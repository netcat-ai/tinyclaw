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

	"tinyclaw/sandbox"
	"tinyclaw/wecom"
	"tinyclaw/wecom/finance"
)

const (
	externalContactCachePrefix = "wecom:contact:external:"
	internalUserCachePrefix    = "wecom:user:internal:"
	groupDetailCachePrefix     = "wecom:group:detail:"
	primeSenderFailCachePrefix = "wecom:user:prime_fail:"
	detailCacheTTL             = time.Hour
	primeSenderFailTTL         = 5 * time.Second
)

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
	egress     *EgressConsumer
	cache      *ttlCache

	groupTriggerKeywords []string
	groupMentionPattern  *regexp.Regexp
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
	egress *EgressConsumer,
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
		egress:               egress,
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
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	seq, err := r.store.GetCursor(ctx, wecomCursorSource, r.cfg.WeComCorpID)
	if err != nil {
		return fmt.Errorf("get cursor from postgres: %w", err)
	}
	for {
		seq, err = r.pullAndDispatch(ctx, seq, 100)
		if err != nil {
			slog.Error("pull and dispatch failed", "err", err)
			pullCycleErrors.Inc()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// roomContext holds the last triggered message info for a room, used in the dispatch phase.
type roomContext struct {
	msg    *WeComMessage
	sender *Identity
}

func (r *Clawman) pullAndDispatch(ctx context.Context, seq, limit int64) (int64, error) {
	chatDataList, err := r.sdk.GetChatData(seq, limit)
	if err != nil {
		return seq, fmt.Errorf("sdk get chat data failed: seq=%d limit=%d err=%w", seq, limit, err)
	}

	if len(chatDataList) == 0 {
		slog.Info("pull completed", "pulled", 0, "dispatched", 0, "seq", seq)
	} else {
		msgPulled.Add(float64(len(chatDataList)))
	}

	// Phase 1: ingest — decrypt, parse, store inbound, advance cursor.
	triggeredRooms := map[string]*roomContext{}
	startSeq := seq
	for _, chatData := range chatDataList {
		if ctx.Err() != nil {
			return seq, ctx.Err()
		}
		seq = chatData.Seq

		raw, err := r.sdk.DecryptData(&chatData)
		if err != nil {
			msgSkipped.WithLabelValues("decrypt_failed").Inc()
			slog.Warn("skip message decrypt failed", "seq", chatData.Seq, "msgid", chatData.MsgID, "err", err)
			continue
		}

		var msg WeComMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			slog.Warn("skip invalid message json", "seq", chatData.Seq, "msgid", chatData.MsgID, "err", err)
			continue
		}
		msg.RawContent = string(raw)
		if msg.From == "" || len(msg.ToList) == 0 {
			slog.Warn("skip invalid message without from/tolist", "seq", chatData.Seq, "msgid", chatData.MsgID)
			continue
		}
		if r.shouldSkipArchivedMessage(&msg) {
			msgSkipped.WithLabelValues("bot_self").Inc()
			continue
		}
		roomID := msg.RoomID
		if msg.RoomID == "" {
			roomID = msg.From
		}

		text, err := extractWeComMessageText(msg.RawContent)
		if err != nil {
			slog.Warn("skip unsupported wecom message payload", "msgid", msg.MsgID, "room_id", roomID, "err", err)
			continue
		}

		sender, err := r.resolveSenderIdentity(ctx, &msg)
		if err != nil {
			continue
		}

		_, err = r.store.StoreInboundMessage(ctx, InboundMessageRecord{
			ID:            "in:" + msg.MsgID,
			TenantID:      r.cfg.WeComCorpID,
			RoomID:        roomID,
			PlatformMsgID: msg.MsgID,
			SenderID:      msg.From,
			SenderName:    sender.Name,
			Content:       text,
			RawPayload:    msg.RawContent,
			CreatedAt:     time.UnixMilli(msg.MsgTime),
		})
		if err != nil {
			return seq, fmt.Errorf("store inbound message for room %s: %w", roomID, err)
		}

		if r.shouldProcessStoredMessage(&msg, text) {
			msgCopy := msg
			triggeredRooms[roomID] = &roomContext{msg: &msgCopy, sender: sender}
		} else {
			msgSkipped.WithLabelValues("no_trigger").Inc()
		}
	}

	// Advance cursor once after all messages are ingested.
	if seq > startSeq {
		if err := r.store.SetCursor(ctx, wecomCursorSource, r.cfg.WeComCorpID, seq); err != nil {
			return seq, fmt.Errorf("set cursor in postgres: %w", err)
		}
	}

	// Phase 2: dispatch — one agent call per room with pending messages.
	// Include rooms from DB that may have been left pending by a previous failed dispatch.
	pendingRooms, err := r.store.ListPendingRooms(ctx, r.cfg.WeComCorpID)
	if err != nil {
		slog.Error("list pending rooms failed", "err", err)
	}
	for _, roomID := range pendingRooms {
		if _, ok := triggeredRooms[roomID]; !ok {
			triggeredRooms[roomID] = nil
		}
	}

	for roomID, rc := range triggeredRooms {
		if ctx.Err() != nil {
			return seq, ctx.Err()
		}

		pendingMessages, err := r.store.ListPendingInboundMessages(ctx, r.cfg.WeComCorpID, roomID)
		if err != nil {
			slog.Error("list pending inbound messages failed", "room_id", roomID, "err", err)
			continue
		}
		if len(pendingMessages) == 0 {
			continue
		}

		// For rooms recovered from DB (no roomContext), derive info from the last pending message.
		lastPending := pendingMessages[len(pendingMessages)-1]
		if rc == nil {
			originalRoomID := roomID
			if roomID == lastPending.SenderID {
				originalRoomID = ""
			}
			rc = &roomContext{
				msg: &WeComMessage{
					MsgID:  lastPending.PlatformMsgID,
					From:   lastPending.SenderID,
					RoomID: originalRoomID,
				},
				sender: &Identity{UserID: lastPending.SenderID, Name: lastPending.SenderName},
			}
		}

		targetName, err := r.resolveRoutingTarget(ctx, rc.msg, rc.sender)
		if err != nil {
			slog.Error("resolve routing target failed", "room_id", roomID, "err", err)
			continue
		}

		query := formatInboundMessagesForAgent(pendingMessages)
		sandboxStart := time.Now()
		reply, err := r.orch.InvokeAgent(ctx, roomID, sandbox.AgentRequest{
			Query:    query,
			MsgID:    rc.msg.MsgID,
			RoomID:   roomID,
			TenantID: r.cfg.WeComCorpID,
			ChatType: chatTypeForRoom(rc.msg.RoomID),
		})
		sandboxDuration.Observe(time.Since(sandboxStart).Seconds())
		if err != nil {
			sandboxInvocations.WithLabelValues("error").Inc()
			slog.Error("invoke sandbox failed", "room_id", roomID, "err", err)
			continue
		}
		sandboxInvocations.WithLabelValues("ok").Inc()
		msgDispatched.Inc()

		pendingIDs := make([]string, 0, len(pendingMessages))
		for _, pending := range pendingMessages {
			pendingIDs = append(pendingIDs, pending.ID)
		}
		if err := r.store.StoreOutboundMessage(ctx, pendingIDs, OutboundMessageRecord{
			ID:         "out:" + rc.msg.MsgID,
			TenantID:   r.cfg.WeComCorpID,
			RoomID:     roomID,
			Content:    reply.Stdout,
			TargetName: targetName,
			CreatedAt:  time.Now().UTC(),
		}); err != nil {
			slog.Error("store outbound message failed", "room_id", roomID, "err", err)
			continue
		}
	}

	return seq, nil
}

func (r *Clawman) shouldProcessStoredMessage(msg *WeComMessage, text string) bool {
	if msg == nil {
		return false
	}
	if msg.RoomID == "" {
		return true
	}
	return r.matchesGroupTrigger(text)
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

func formatInboundMessagesForAgent(messages []InboundMessageRecord) string {
	if len(messages) == 0 {
		return ""
	}
	if len(messages) == 1 {
		return messages[0].Content
	}

	var b strings.Builder
	b.WriteString("以下是当前会话自上次处理以来的未处理消息，请结合上下文回复最后的用户请求：\n")
	for _, message := range messages {
		sender := strings.TrimSpace(message.SenderName)
		if sender == "" {
			sender = message.SenderID
		}
		b.WriteString("[")
		b.WriteString(message.CreatedAt.UTC().Format(time.RFC3339))
		b.WriteString("] ")
		b.WriteString(sender)
		b.WriteString(": ")
		b.WriteString(message.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
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

	return sender.Name, nil
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

func chatTypeForRoom(roomID string) string {
	if roomID == "" {
		return "direct"
	}
	return "group"
}
