package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"tinyclaw/sandbox"
	"tinyclaw/wecom"
	"tinyclaw/wecom/finance"

	clawmanv1 "tinyclaw/clawman/v1"
)

const (
	externalContactCachePrefix = "wecom:contact:external:"
	internalUserCachePrefix    = "wecom:user:internal:"
	groupDetailCachePrefix     = "wecom:group:detail:"
	primeSenderFailCachePrefix = "wecom:user:prime_fail:"
	detailCacheTTL             = time.Hour
	primeSenderFailTTL         = 5 * time.Second
	ingestPollInterval         = 3 * time.Second
	dispatchPollInterval       = time.Second
	coldStartWindow            = 10 * time.Minute
	dispatchRoomTimeout        = 6 * time.Minute
	pendingDispatchWindow      = 10 * time.Minute
)

var errFatalIngest = errors.New("fatal ingest error")

// Identity represents a resolved WeCom user identity.
type Identity struct {
	UserID   string `json:"userid"`
	Name     string `json:"name"`
	Type     string `json:"type"`      // "employee" | "external" | "guest"
	CorpName string `json:"corp_name"` // 外部联系人所属企业
}

// GroupDetail represents resolved metadata for a room-level group chat.
type GroupDetail struct {
	ChatID string `json:"chat_id"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Type   string `json:"type"` // "customer_group" | "internal_group"
}

type Clawman struct {
	cfg        Config
	store      *Store
	sdk        *finance.SDK
	contactAPI *wecom.Client
	archiveAPI *wecom.Client
	orch       *sandbox.Orchestrator
	gateway    *MessageGateway
	cache      *ttlCache

	groupTriggerKeywords []string
	groupMentionPattern  *regexp.Regexp
	coldStartCutoff      *time.Time
	lastSeq              int64
}

type WeComMessage struct {
	MsgID      string   `json:"msgid"`
	Action     string   `json:"action"`
	From       string   `json:"from"`
	ToList     []string `json:"tolist"`
	RoomID     string   `json:"roomid"`
	MsgTime    int64    `json:"msgtime"`
	MsgType    string   `json:"msgtype"`
	RawContent string   `json:"-"`
}

func NewClawman(
	cfg Config,
	store *Store,
	orch *sandbox.Orchestrator,
	gateway *MessageGateway,
) (*Clawman, error) {
	if cfg.WeComCorpID == "" || cfg.WeComCorpSecret == "" || cfg.WeComPrivateKey == "" {
		return nil, fmt.Errorf("WECOM_CORP_ID/WECOM_CORP_SECRET/WECOM_RSA_PRIVATE_KEY are required")
	}

	sdk, err := finance.NewSDK(
		cfg.WeComCorpID,
		cfg.WeComCorpSecret,
		cfg.WeComPrivateKey,
		"",
		"",
		10,
	)
	if err != nil {
		return nil, fmt.Errorf("init wecom sdk: %w", err)
	}

	var contactAPI *wecom.Client
	if cfg.WeComContactSecret != "" {
		contactAPI = wecom.NewClient(cfg.WeComCorpID, cfg.WeComContactSecret)
	}
	archiveAPI := wecom.NewClient(cfg.WeComCorpID, cfg.WeComCorpSecret)

	return &Clawman{
		cfg:                  cfg,
		store:                store,
		sdk:                  sdk,
		contactAPI:           contactAPI,
		archiveAPI:           archiveAPI,
		orch:                 orch,
		gateway:              gateway,
		cache:                newTTLCache(),
		groupTriggerKeywords: normalizeTriggerTerms(cfg.WeComGroupTriggerKeywords),
		groupMentionPattern:  buildGroupMentionPattern(cfg.WeComGroupTriggerMentions),
	}, nil
}

func (r *Clawman) Close() {
	if r.sdk != nil {
		r.sdk.Free()
	}
	if r.orch != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.orch.Close(ctx); err != nil {
			slog.Error("close sandbox orchestrator failed", "err", err)
		}
	}
}

func (r *Clawman) Run(ctx context.Context) error {
	lastSeq, hasMessages, err := r.store.GetMaxSeq(ctx, r.cfg.WeComCorpID)
	if err != nil {
		return fmt.Errorf("get startup seq: %w", err)
	}
	r.lastSeq = lastSeq
	if !hasMessages {
		cutoff := time.Now().UTC().Add(-coldStartWindow)
		r.coldStartCutoff = &cutoff
		slog.Info("cold start window enabled", "cutoff", cutoff.Format(time.RFC3339))
	}

	errCh := make(chan error, 2)

	go func() {
		errCh <- r.runIngestLoop(ctx)
	}()
	go func() {
		errCh <- r.runDispatchLoop(ctx)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
	return nil
}

type roomContext struct {
	msg    *WeComMessage
	sender *Identity
}

func (r *Clawman) runIngestLoop(ctx context.Context) error {
	ticker := time.NewTicker(ingestPollInterval)
	defer ticker.Stop()

	for {
		committedSeq, err := r.pullOnce(ctx, 100)
		if committedSeq > r.lastSeq {
			r.lastSeq = committedSeq
		}
		if err != nil {
			if errors.Is(err, errFatalIngest) {
				return err
			}
			slog.Error("ingest pull failed", "err", err)
			pullCycleErrors.Inc()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Clawman) runDispatchLoop(ctx context.Context) error {
	ticker := time.NewTicker(dispatchPollInterval)
	defer ticker.Stop()

	for {
		if err := r.dispatchPendingRooms(ctx); err != nil {
			slog.Error("dispatch pending rooms failed", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Clawman) pullOnce(ctx context.Context, limit int64) (int64, error) {
	seq := r.lastSeq
	chatDataList, err := r.sdk.GetChatData(seq, limit)
	if err != nil {
		return seq, fmt.Errorf("sdk get chat data failed: seq=%d limit=%d err=%w", seq, limit, err)
	}
	r.recordPullMetrics(seq, chatDataList)

	return r.ingestBatch(ctx, seq, chatDataList)
}

func (r *Clawman) recordPullMetrics(seq int64, chatDataList []finance.ChatData) {
	if len(chatDataList) == 0 {
		slog.Info("pull completed", "pulled", 0, "seq", seq)
		return
	}
	msgPulled.Add(float64(len(chatDataList)))
}

func (r *Clawman) ingestBatch(ctx context.Context, seq int64, chatDataList []finance.ChatData) (int64, error) {
	committedSeq := seq
	for _, chatData := range chatDataList {
		if ctx.Err() != nil {
			return committedSeq, ctx.Err()
		}
		if err := r.ingestChatData(ctx, chatData); err != nil {
			return committedSeq, err
		}
		committedSeq = chatData.Seq
	}
	return committedSeq, nil
}

func (r *Clawman) ingestChatData(ctx context.Context, chatData finance.ChatData) error {
	record, promoteBuffered, err := r.buildMessageRecord(ctx, chatData)
	if err != nil {
		return err
	}
	if _, err := r.store.StoreMessage(ctx, record, promoteBuffered); err != nil {
		return fmt.Errorf("store message seq=%d: %w", chatData.Seq, err)
	}
	return nil
}

func (r *Clawman) buildMessageRecord(ctx context.Context, chatData finance.ChatData) (MessageRecord, bool, error) {
	raw, err := r.sdk.DecryptData(&chatData)
	if err != nil {
		msgSkipped.WithLabelValues("decrypt_failed").Inc()
		return MessageRecord{}, false, fmt.Errorf("%w: decrypt chat data seq=%d msgid=%s: %v", errFatalIngest, chatData.Seq, chatData.MsgID, err)
	}

	var msg WeComMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		msgSkipped.WithLabelValues("invalid_json").Inc()
		return MessageRecord{}, false, fmt.Errorf("%w: invalid message json seq=%d msgid=%s: %v", errFatalIngest, chatData.Seq, chatData.MsgID, err)
	}
	msg.RawContent = string(raw)

	if err := validateParsedMessage(&msg); err != nil {
		msgSkipped.WithLabelValues("invalid_shape").Inc()
		return MessageRecord{}, false, fmt.Errorf(
			"%w: invalid message shape seq=%d msgid=%s: %v",
			errFatalIngest,
			chatData.Seq,
			msg.MsgID,
			err,
		)
	}

	roomID := strings.TrimSpace(msg.RoomID)
	if roomID == "" {
		roomID = msg.From
	}

	record := MessageRecord{
		Seq:       chatData.Seq,
		TenantID:  r.cfg.WeComCorpID,
		MsgID:     msg.MsgID,
		RoomID:    roomID,
		FromID:    msg.From,
		Payload:   string(raw),
		Status:    statusIgnored,
		MsgTime:   time.UnixMilli(msg.MsgTime).UTC(),
		CreatedAt: time.Now().UTC(),
	}

	if name, resolveErr := r.resolveSenderName(ctx, &msg); resolveErr == nil {
		record.FromName = name
	}

	status, promoteBuffered, err := r.statusForMessage(&msg, msg.RawContent)
	if err != nil {
		msgSkipped.WithLabelValues("unsupported_payload").Inc()
		slog.Warn("skip unsupported wecom message payload", "msgid", msg.MsgID, "room_id", record.RoomID, "err", err)
		return record, false, nil
	}
	if r.coldStartCutoff != nil && !record.MsgTime.IsZero() && record.MsgTime.Before(*r.coldStartCutoff) {
		msgSkipped.WithLabelValues("cold_start").Inc()
		record.Status = statusIgnored
		return record, false, nil
	}

	record.Status = status
	return record, promoteBuffered, nil
}

func validateParsedMessage(msg *WeComMessage) error {
	if msg == nil {
		return fmt.Errorf("message is nil")
	}
	if strings.TrimSpace(msg.MsgID) == "" {
		return fmt.Errorf("msgid is empty")
	}
	if strings.TrimSpace(msg.From) == "" {
		return fmt.Errorf("from is empty")
	}
	if len(msg.ToList) == 0 {
		return fmt.Errorf("tolist is empty")
	}
	return nil
}

func (r *Clawman) dispatchPendingRooms(ctx context.Context) error {
	pendingRooms, err := r.store.ListPendingRooms(ctx, r.cfg.WeComCorpID)
	if err != nil {
		return err
	}
	for _, roomID := range pendingRooms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.dispatchRoom(ctx, roomID)
	}
	return nil
}

func (r *Clawman) dispatchRoom(ctx context.Context, roomID string) {
	if r.gateway == nil {
		slog.Error("grpc gateway not configured", "room_id", roomID)
		return
	}
	pendingMessages, err := r.store.ListPendingMessages(ctx, r.cfg.WeComCorpID, roomID)
	if err != nil {
		slog.Error("list pending messages failed", "room_id", roomID, "err", err)
		return
	}
	if len(pendingMessages) == 0 {
		return
	}

	recentMessages, staleSeqs := partitionDispatchablePending(pendingMessages, time.Now().UTC().Add(-pendingDispatchWindow))
	if len(staleSeqs) > 0 {
		if err := r.store.MarkMessagesIgnored(ctx, staleSeqs); err != nil {
			slog.Error("mark stale pending messages ignored failed", "room_id", roomID, "err", err)
			return
		}
		slog.Warn("ignored stale pending messages", "room_id", roomID, "count", len(staleSeqs))
	}
	if len(recentMessages) == 0 {
		return
	}

	rc, err := r.buildRoomContext(ctx, roomID, recentMessages)
	if err != nil {
		slog.Error("build room context failed", "room_id", roomID, "err", err)
		return
	}

	targetName, err := r.resolveRoutingTarget(ctx, rc.msg, rc.sender)
	if err != nil {
		slog.Error("resolve routing target failed", "room_id", roomID, "err", err)
		return
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, dispatchRoomTimeout)
	defer cancel()

	reply, err := r.invokeRoomAgent(dispatchCtx, roomID, rc.msg, recentMessages)
	if err != nil {
		slog.Error("invoke sandbox failed", "room_id", roomID, "err", err)
		return
	}

	pendingSeqs := make([]int64, 0, len(recentMessages))
	var maxSeq int64
	for _, pending := range recentMessages {
		pendingSeqs = append(pendingSeqs, pending.Seq)
		if pending.Seq > maxSeq {
			maxSeq = pending.Seq
		}
	}

	if err := r.enqueueJob(ctx, targetName, reply, maxSeq); err != nil {
		slog.Error("enqueue reply job failed", "room_id", roomID, "target", targetName, "err", err)
		if resetErr := r.store.MarkMessagesPending(ctx, pendingSeqs); resetErr != nil {
			slog.Error("reset messages pending after enqueue failure failed", "room_id", roomID, "err", resetErr)
		}
		return
	}

	if err := r.store.MarkMessagesDone(ctx, pendingSeqs); err != nil {
		slog.Error("mark messages done failed", "room_id", roomID, "err", err)
	}
}

func partitionDispatchablePending(messages []MessageRecord, cutoff time.Time) ([]MessageRecord, []int64) {
	if len(messages) == 0 {
		return nil, nil
	}

	dispatchable := make([]MessageRecord, 0, len(messages))
	staleSeqs := make([]int64, 0)
	for _, message := range messages {
		eventTime := message.MsgTime
		if eventTime.IsZero() {
			eventTime = message.CreatedAt
		}
		if !eventTime.IsZero() && eventTime.Before(cutoff) {
			staleSeqs = append(staleSeqs, message.Seq)
			continue
		}
		dispatchable = append(dispatchable, message)
	}
	return dispatchable, staleSeqs
}

func (r *Clawman) enqueueJob(ctx context.Context, targetName, content string, maxSeq int64) error {
	botID := strings.TrimSpace(r.cfg.WeComBotID)
	if botID == "" {
		return fmt.Errorf("wecom bot id is empty")
	}
	if strings.TrimSpace(targetName) == "" {
		return fmt.Errorf("reply target is empty")
	}
	content = strings.TrimSpace(content)
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("reply content is empty")
	}
	_, err := r.store.EnqueueJob(ctx, botID, targetName, content, maxSeq)
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}
	return nil
}

func (r *Clawman) buildRoomContext(ctx context.Context, roomID string, pendingMessages []MessageRecord) (*roomContext, error) {
	if len(pendingMessages) == 0 {
		return &roomContext{}, nil
	}

	lastPending := pendingMessages[len(pendingMessages)-1]
	msgRoomID := roomID
	if roomID == lastPending.FromID {
		msgRoomID = ""
	}
	rc := &roomContext{
		msg: &WeComMessage{
			MsgID:  lastPending.MsgID,
			From:   lastPending.FromID,
			RoomID: msgRoomID,
		},
	}
	if lastPending.FromID != "" {
		rc.sender = &Identity{UserID: lastPending.FromID, Name: lastPending.FromName}
		if rc.sender.Name == "" {
			if ident, err := r.Resolve(ctx, lastPending.FromID); err == nil {
				rc.sender = ident
			}
		}
	}
	return rc, nil
}

func (r *Clawman) invokeRoomAgent(ctx context.Context, roomID string, msg *WeComMessage, pendingMessages []MessageRecord) (string, error) {
	if r.orch == nil {
		return "", fmt.Errorf("sandbox integration not configured")
	}
	if r.gateway == nil {
		return "", fmt.Errorf("grpc gateway not configured")
	}

	sandboxStart := time.Now()
	if _, err := r.orch.EnsureRoom(ctx, roomID); err != nil {
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		sandboxInvocations.WithLabelValues("error").Inc()
		return "", err
	}

	seqs := make([]int64, 0, len(pendingMessages))
	for _, message := range pendingMessages {
		seqs = append(seqs, message.Seq)
	}

	requestID := uuid.NewString()
	responseCh, err := r.gateway.SendBatch(ctx, roomID, &clawmanv1.Message{
		Kind:      "messages",
		RequestId: requestID,
		Messages:  buildProtoMessages(pendingMessages),
	})
	if err != nil {
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		sandboxInvocations.WithLabelValues("error").Inc()
		return "", err
	}

	if err := r.store.MarkMessagesSent(ctx, seqs); err != nil {
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		sandboxInvocations.WithLabelValues("error").Inc()
		return "", fmt.Errorf("mark messages sent: %w", err)
	}

	select {
	case <-ctx.Done():
		_ = r.store.MarkMessagesPending(context.Background(), seqs)
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		sandboxInvocations.WithLabelValues("error").Inc()
		return "", ctx.Err()
	case resp := <-responseCh:
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		if resp.err != nil {
			sandboxInvocations.WithLabelValues("error").Inc()
			if resetErr := r.store.MarkMessagesPending(ctx, seqs); resetErr != nil {
				slog.Error("reset messages pending after sandbox error failed", "room_id", roomID, "err", resetErr)
			}
			return "", resp.err
		}
		sandboxInvocations.WithLabelValues("ok").Inc()
		msgDispatched.Inc()
		return resp.output, nil
	}
}

func (r *Clawman) statusForMessage(msg *WeComMessage, payload string) (string, bool, error) {
	if msg == nil {
		return statusIgnored, false, nil
	}
	if r.shouldSkipArchivedMessage(msg) {
		msgSkipped.WithLabelValues("bot_self").Inc()
		return statusIgnored, false, nil
	}

	text, err := extractWeComMessageText(payload)
	if err != nil {
		return statusIgnored, false, err
	}
	if msg.RoomID == "" {
		return statusPending, false, nil
	}
	if r.matchesGroupTrigger(text) {
		return statusPending, true, nil
	}
	return statusBuffered, false, nil
}

func (r *Clawman) matchesGroupTrigger(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if r.groupMentionPattern != nil && r.groupMentionPattern.MatchString(trimmed) {
		return true
	}

	normalized := strings.ToLower(trimmed)
	for _, keyword := range r.groupTriggerKeywords {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return false
}

func (r *Clawman) shouldSkipArchivedMessage(msg *WeComMessage) bool {
	if msg == nil {
		return false
	}
	if r.cfg.WeComBotID != "" && msg.From == r.cfg.WeComBotID {
		return true
	}
	return false
}

func normalizeTriggerTerms(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if slices.Contains(normalized, value) {
			continue
		}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func buildGroupMentionPattern(values []string) *regexp.Regexp {
	normalized := normalizeTriggerTerms(values)
	if len(normalized) == 0 {
		return nil
	}

	parts := make([]string, 0, len(normalized))
	for _, value := range normalized {
		parts = append(parts, regexp.QuoteMeta(value))
	}
	return regexp.MustCompile(`(?i)(?:^|[\s\p{P}])@(?:` + strings.Join(parts, "|") + `)(?:$|[\s\p{P}])`)
}

func buildProtoMessages(messages []MessageRecord) []*clawmanv1.AgentMessage {
	if len(messages) == 0 {
		return nil
	}

	out := make([]*clawmanv1.AgentMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, &clawmanv1.AgentMessage{
			Seq:      message.Seq,
			Msgid:    message.MsgID,
			RoomId:   message.RoomID,
			FromId:   message.FromID,
			FromName: message.FromName,
			MsgTime:  message.MsgTime.UTC().Format(time.RFC3339),
			Payload:  message.Payload,
		})
	}
	return out
}

func (r *Clawman) resolveSenderName(ctx context.Context, msg *WeComMessage) (string, error) {
	if msg == nil || strings.TrimSpace(msg.From) == "" {
		return "", fmt.Errorf("sender id is empty")
	}
	ident, err := r.Resolve(ctx, msg.From)
	if err != nil {
		slog.Warn("resolve sender name on ingest failed", "from", msg.From, "msgid", msg.MsgID, "err", err)
		return "", err
	}
	return ident.Name, nil
}

func (r *Clawman) resolveSenderIdentity(ctx context.Context, msg *WeComMessage) (*Identity, error) {
	if msg == nil {
		return nil, fmt.Errorf("message is nil")
	}

	failKey := primeSenderFailCachePrefix + msg.From
	if r.cache.Has(failKey) {
		return nil, fmt.Errorf("sender identity for %s is temporarily suppressed after previous failure", msg.From)
	}

	ident, err := r.Resolve(ctx, msg.From)
	if err != nil {
		r.cache.Set(failKey, []byte(err.Error()), primeSenderFailTTL)
		slog.Error("resolve sender on receive failed", "from", msg.From, "msgid", msg.MsgID, "err", err)
		return nil, err
	}
	return ident, nil
}

func (r *Clawman) primeSenderIdentity(ctx context.Context, msg *WeComMessage) bool {
	_, err := r.resolveSenderIdentity(ctx, msg)
	return err == nil
}

func (r *Clawman) resolveRoutingTarget(ctx context.Context, msg *WeComMessage, sender *Identity) (targetName string, err error) {
	if msg.RoomID != "" {
		group, err := r.ResolveGroup(ctx, msg.RoomID, sender)
		if err != nil {
			return "", err
		}
		return group.Name, nil
	}

	if sender != nil && strings.TrimSpace(sender.Name) != "" {
		return sender.Name, nil
	}
	if msg != nil && strings.TrimSpace(msg.From) != "" {
		return msg.From, nil
	}
	return "", fmt.Errorf("direct routing target is empty")
}

// Resolve resolves a WeCom sender ID to an Identity.
// Direct messages use sender identity to decide between external contact and internal user APIs.
func (r *Clawman) Resolve(ctx context.Context, id string) (*Identity, error) {
	cacheKey := internalUserCachePrefix + id
	if isExternalUserID(id) {
		cacheKey = externalContactCachePrefix + id
	}

	if cached, ok := r.cache.Get(cacheKey); ok {
		ident := &Identity{}
		if json.Unmarshal(cached, ident) == nil {
			return ident, nil
		}
	}

	ident, err := r.resolveIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(ident); err == nil {
		r.cache.Set(cacheKey, data, detailCacheTTL)
	}
	return ident, nil
}

func (r *Clawman) resolveIdentity(ctx context.Context, id string) (*Identity, error) {
	if isExternalUserID(id) {
		return r.resolveExternal(ctx, id)
	}
	return r.resolveInternalUser(ctx, id)
}

func (r *Clawman) resolveExternal(ctx context.Context, id string) (*Identity, error) {
	if r.contactAPI == nil {
		return nil, fmt.Errorf("contact api not configured")
	}
	contact, err := r.contactAPI.GetExternalContact(ctx, id)
	if err != nil {
		var apiErr *wecom.APIError
		if errors.As(err, &apiErr) && apiErr.Code == 84061 {
			slog.Warn("not external contact, skipping", "id", id)
			return &Identity{UserID: id, Name: id, Type: "unknown"}, nil
		}
		return nil, fmt.Errorf("get external contact %s: %w", id, err)
	}
	return &Identity{
		UserID:   id,
		Name:     contact.Name,
		Type:     "external",
		CorpName: contact.CorpName,
	}, nil
}

func (r *Clawman) resolveInternalUser(ctx context.Context, id string) (*Identity, error) {
	if r.contactAPI == nil {
		return nil, fmt.Errorf("contact api not configured")
	}
	user, err := r.contactAPI.GetUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get internal user %s: %w", id, err)
	}
	return &Identity{
		UserID: user.UserID,
		Name:   user.Name,
		Type:   "employee",
	}, nil
}

// ResolveGroup resolves a room ID to customer-group or internal-group metadata.
// When sender is known, it uses sender type to select the matching WeCom API.
func (r *Clawman) ResolveGroup(ctx context.Context, roomID string, sender *Identity) (*GroupDetail, error) {
	if cached, ok := r.cache.Get(groupDetailCachePrefix + roomID); ok {
		detail := &GroupDetail{}
		if json.Unmarshal(cached, detail) == nil {
			return detail, nil
		}
	}
	detail, err := r.resolveGroup(ctx, roomID, sender)
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(detail); err == nil {
		r.cache.Set(groupDetailCachePrefix+roomID, data, detailCacheTTL)
	}
	return detail, nil
}

func (r *Clawman) resolveGroup(ctx context.Context, roomID string, sender *Identity) (*GroupDetail, error) {
	if sender != nil {
		switch sender.Type {
		case "external", "guest":
			return r.resolveCustomerGroup(ctx, roomID)
		}
	}

	// Try internal group first, fallback to customer group.
	internalGroup, internalErr := r.resolveInternalGroup(ctx, roomID)
	if internalErr == nil {
		return internalGroup, nil
	}
	return r.resolveCustomerGroup(ctx, roomID)
}

func (r *Clawman) resolveInternalGroup(ctx context.Context, roomID string) (*GroupDetail, error) {
	if r.archiveAPI != nil {
		group, err := r.archiveAPI.GetArchiveGroupChat(ctx, roomID)
		if err == nil {
			return &GroupDetail{
				ChatID: group.ChatID,
				Name:   group.Name,
				Type:   "internal_group",
			}, nil
		}
		return nil, fmt.Errorf("resolve internal group %s: %w", roomID, err)
	}
	return nil, fmt.Errorf("archive api not configured")
}

func (r *Clawman) resolveCustomerGroup(ctx context.Context, roomID string) (*GroupDetail, error) {
	if r.contactAPI == nil {
		return nil, fmt.Errorf("contact api not configured")
	}
	group, err := r.contactAPI.GetGroupChat(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("resolve customer group %s: %w", roomID, err)
	}
	return &GroupDetail{
		ChatID: group.ChatID,
		Name:   group.Name,
		Owner:  group.Owner,
		Type:   "customer_group",
	}, nil
}

func isExternalUserID(id string) bool {
	return strings.HasPrefix(id, "wm") || strings.HasPrefix(id, "wo")
}
