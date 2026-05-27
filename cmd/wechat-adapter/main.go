package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"tinyclaw/internal/envfile"
)

const (
	defaultClawmanBaseURL = "http://127.0.0.1:8081"
	defaultWechatChannel  = "wechat"
	defaultWechatGroupID  = "50261801724@chatroom"
	defaultWechatGroup    = "测试群"
	defaultPollInterval   = 3 * time.Second
	defaultPollLimit      = 200
	defaultReadMode       = "auto"
	defaultTargetChats    = ""
)

type config struct {
	ClawmanBaseURL string
	ClawmanToken   string
	WXBin          string
	GroupID        string
	GroupName      string
	HistoryChat    string
	PollInterval   time.Duration
	PollLimit      int
	ReadMode       string
	TriggerPolicy  json.RawMessage
	Once           bool
	SelfSenders    map[string]bool
	TargetChats    map[string]bool
	TargetMembers  map[string]bool
}

type wxMessage struct {
	Chat      string `json:"chat"`
	ChatType  string `json:"chat_type"`
	Content   string `json:"content"`
	IsGroup   bool   `json:"is_group"`
	LocalID   int64  `json:"local_id"`
	Sender    string `json:"sender"`
	Time      string `json:"time"`
	Timestamp int64  `json:"timestamp"`
	Type      string `json:"type"`
	Username  string `json:"username"`
}

type wxMember struct {
	Display  string `json:"display"`
	Username string `json:"username"`
}

type roomResponse struct {
	Room struct {
		ID int64 `json:"id"`
	} `json:"room"`
}

type messageRoomIdentity struct {
	ChannelRoomID   string
	ChannelRoomType string
	DisplayName     string
}

type adapter struct {
	cfg              config
	client           *http.Client
	roomIDs          map[string]int64
	targetGroupCache map[string]bool
}

func main() {
	if err := envfile.Load(".env"); err != nil {
		slog.Warn("load .env failed", "err", err)
	}
	if err := run(context.Background()); err != nil {
		slog.Error("wechat adapter stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a := &adapter{
		cfg:              cfg,
		client:           &http.Client{Timeout: 15 * time.Second},
		roomIDs:          map[string]int64{},
		targetGroupCache: map[string]bool{},
	}
	slog.Info(
		"wechat adapter starting",
		"group_id", cfg.GroupID,
		"group_name", cfg.GroupName,
		"interval", cfg.PollInterval,
		"target_chats", mapKeys(cfg.TargetChats),
		"target_members", mapKeys(cfg.TargetMembers),
	)
	if cfg.Once {
		return a.pollOnce(ctx)
	}
	for {
		if err := a.pollOnce(ctx); err != nil {
			slog.Error("wechat poll failed", "err", err)
		}
		timer := time.NewTimer(cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func loadConfig() (config, error) {
	interval, err := time.ParseDuration(envOrDefault("WECHAT_POLL_INTERVAL", defaultPollInterval.String()))
	if err != nil {
		return config{}, fmt.Errorf("parse WECHAT_POLL_INTERVAL: %w", err)
	}
	limit, err := strconv.Atoi(envOrDefault("WECHAT_POLL_LIMIT", strconv.Itoa(defaultPollLimit)))
	if err != nil || limit <= 0 {
		return config{}, fmt.Errorf("WECHAT_POLL_LIMIT must be a positive integer")
	}
	triggerPolicy := json.RawMessage(envOrDefault("WECHAT_TRIGGER_POLICY", `{"mode":"always"}`))
	if !json.Valid(triggerPolicy) {
		return config{}, fmt.Errorf("WECHAT_TRIGGER_POLICY must be valid JSON")
	}
	cfg := config{
		ClawmanBaseURL: strings.TrimRight(envOrDefault("CLAWMAN_BASE_URL", defaultClawmanBaseURL), "/"),
		ClawmanToken:   strings.TrimSpace(os.Getenv("CLAWMAN_API_TOKEN")),
		WXBin:          envOrDefault("WECHAT_WX_BIN", "wx"),
		GroupID:        envOrDefault("WECHAT_GROUP_ID", defaultWechatGroupID),
		GroupName:      envOrDefault("WECHAT_GROUP_NAME", defaultWechatGroup),
		HistoryChat:    strings.TrimSpace(os.Getenv("WECHAT_HISTORY_CHAT")),
		PollInterval:   interval,
		PollLimit:      limit,
		ReadMode:       strings.ToLower(envOrDefault("WECHAT_READ_MODE", defaultReadMode)),
		TriggerPolicy:  triggerPolicy,
		Once:           parseBoolEnv("WECHAT_ONCE"),
		SelfSenders:    parseSelfSendersEnv(),
		TargetChats:    parseNameSetEnv("WECHAT_TARGET_CHATS", defaultTargetChats),
		TargetMembers:  parseNameSetEnv("WECHAT_TARGET_MEMBERS", ""),
	}
	if cfg.ClawmanToken == "" {
		return config{}, fmt.Errorf("CLAWMAN_API_TOKEN is required")
	}
	if cfg.ReadMode != "auto" && cfg.ReadMode != "history" && cfg.ReadMode != "new-messages" {
		return config{}, fmt.Errorf("WECHAT_READ_MODE must be auto, history, or new-messages")
	}
	return cfg, nil
}

func (a *adapter) ensureRoom(ctx context.Context, msg wxMessage) (int64, error) {
	identity := roomIdentity(msg)
	if identity.ChannelRoomID == "" {
		identity.ChannelRoomID = a.cfg.GroupID
	}
	if identity.DisplayName == "" {
		identity.DisplayName = a.cfg.GroupName
	}
	if identity.ChannelRoomType == "" {
		identity.ChannelRoomType = "direct"
	}
	key := identity.ChannelRoomID
	if id := a.roomIDs[key]; id > 0 {
		return id, nil
	}
	reqBody := map[string]any{
		"channel":           defaultWechatChannel,
		"channel_room_id":   identity.ChannelRoomID,
		"channel_room_type": identity.ChannelRoomType,
		"display_name":      identity.DisplayName,
		"outbound_alias":    identity.DisplayName,
		"agent_enabled":     true,
		"trigger_policy":    json.RawMessage(a.cfg.TriggerPolicy),
	}
	var resp roomResponse
	if err := a.postJSON(ctx, "/api/rooms", reqBody, &resp); err != nil {
		return 0, fmt.Errorf("register wechat room %s: %w", key, err)
	}
	if resp.Room.ID <= 0 {
		return 0, fmt.Errorf("register wechat room %s returned empty room id", key)
	}
	if a.roomIDs == nil {
		a.roomIDs = map[string]int64{}
	}
	a.roomIDs[key] = resp.Room.ID
	return resp.Room.ID, nil
}

func (a *adapter) pollOnce(ctx context.Context) error {
	messages, err := a.readNewMessages(ctx)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	sortMessages(messages)
	seen := map[string]bool{}
	processed := 0
	for _, msg := range messages {
		target, err := a.isTargetMessage(ctx, msg)
		if err != nil {
			slog.Warn("wechat target check failed", "chat", msg.Chat, "username", msg.Username, "err", err)
			continue
		}
		if !target {
			continue
		}
		sourceID := sourceMessageID(msg)
		if seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		if err := a.createMessage(ctx, msg); err != nil {
			return err
		}
		processed++
	}
	if processed > 0 {
		slog.Info("wechat messages ingested", "count", processed)
	}
	return nil
}

func (a *adapter) readNewMessages(ctx context.Context) ([]wxMessage, error) {
	switch a.cfg.ReadMode {
	case "auto":
		messages, err := a.readHistoryMessages(ctx)
		if err == nil {
			return messages, nil
		}
		slog.Warn("wx history failed; falling back to wx new-messages", "err", err)
		return a.readGlobalNewMessages(ctx)
	case "history":
		return a.readHistoryMessages(ctx)
	case "new-messages":
		return a.readGlobalNewMessages(ctx)
	default:
		return nil, fmt.Errorf("unsupported read mode %q", a.cfg.ReadMode)
	}
}

func (a *adapter) readHistoryMessages(ctx context.Context) ([]wxMessage, error) {
	var lastErr error
	for _, chat := range a.historyChatCandidates() {
		messages, err := a.runWXMessages(ctx, "history", chat, "-n", strconv.Itoa(a.cfg.PollLimit), "--json")
		if err != nil {
			lastErr = err
			continue
		}
		for index := range messages {
			if strings.TrimSpace(messages[index].Username) == "" {
				messages[index].Username = a.cfg.GroupID
			}
			if strings.TrimSpace(messages[index].Chat) == "" {
				messages[index].Chat = a.cfg.GroupName
			}
			if strings.TrimSpace(messages[index].ChatType) == "" {
				messages[index].ChatType = "group"
			}
			messages[index].IsGroup = true
		}
		return messages, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no wx history chat candidate configured")
}

func (a *adapter) historyChatCandidates() []string {
	seen := map[string]bool{}
	var candidates []string
	for _, value := range []string{a.cfg.HistoryChat, a.cfg.GroupName, a.cfg.GroupID} {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		candidates = append(candidates, value)
	}
	return candidates
}

func (a *adapter) readGlobalNewMessages(ctx context.Context) ([]wxMessage, error) {
	return a.runWXMessages(ctx, "new-messages", "-n", strconv.Itoa(a.cfg.PollLimit), "--json")
}

func (a *adapter) runWXMessages(ctx context.Context, args ...string) ([]wxMessage, error) {
	cmd := exec.CommandContext(ctx, a.cfg.WXBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("run wx %s: %s", strings.Join(args, " "), detail)
	}
	var messages []wxMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("decode wx %s: %w", strings.Join(args, " "), err)
	}
	for index := range messages {
		messages[index] = normalizeWXMessage(messages[index])
	}
	return messages, nil
}

func (a *adapter) runWXMembers(ctx context.Context, chat string) ([]wxMember, error) {
	cmd := exec.CommandContext(ctx, a.cfg.WXBin, "members", chat, "--json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("run wx members %s: %s", chat, detail)
	}
	var members []wxMember
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, fmt.Errorf("decode wx members %s: %w", chat, err)
	}
	return members, nil
}

func (a *adapter) isTargetMessage(ctx context.Context, msg wxMessage) (bool, error) {
	if a.matchesTargetChat(msg) {
		return true, nil
	}
	if len(a.cfg.TargetMembers) == 0 {
		return false, nil
	}
	if !isGroupMessage(msg) {
		return a.cfg.TargetMembers[strings.TrimSpace(msg.Username)], nil
	}
	return a.groupHasTargetMember(ctx, msg)
}

func (a *adapter) matchesTargetChat(msg wxMessage) bool {
	if len(a.cfg.TargetChats) == 0 {
		return false
	}
	identity := roomIdentity(msg)
	return a.cfg.TargetChats[strings.TrimSpace(msg.Username)] ||
		a.cfg.TargetChats[strings.TrimSpace(msg.Chat)] ||
		a.cfg.TargetChats[identity.ChannelRoomID] ||
		a.cfg.TargetChats[identity.DisplayName]
}

func (a *adapter) groupHasTargetMember(ctx context.Context, msg wxMessage) (bool, error) {
	identity := roomIdentity(msg)
	key := identity.ChannelRoomID
	if key == "" {
		return false, nil
	}
	if hit, ok := a.targetGroupCache[key]; ok {
		return hit, nil
	}
	if a.targetGroupCache == nil {
		a.targetGroupCache = map[string]bool{}
	}
	var lastErr error
	for _, chat := range memberLookupChats(msg) {
		members, err := a.runWXMembers(ctx, chat)
		if err != nil {
			lastErr = err
			continue
		}
		for _, member := range members {
			if a.cfg.TargetMembers[strings.TrimSpace(member.Username)] {
				a.targetGroupCache[key] = true
				return true, nil
			}
		}
		a.targetGroupCache[key] = false
		return false, nil
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

func memberLookupChats(msg wxMessage) []string {
	seen := map[string]bool{}
	var chats []string
	for _, value := range []string{strings.TrimSpace(msg.Username), strings.TrimSpace(msg.Chat)} {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		chats = append(chats, value)
	}
	return chats
}

func roomIdentity(msg wxMessage) messageRoomIdentity {
	username := strings.TrimSpace(msg.Username)
	chat := strings.TrimSpace(msg.Chat)
	sender := strings.TrimSpace(msg.Sender)
	if isGroupMessage(msg) {
		return messageRoomIdentity{
			ChannelRoomID:   firstNonEmpty(username, chat, sender),
			ChannelRoomType: "group",
			DisplayName:     firstNonEmpty(chat, username),
		}
	}
	return messageRoomIdentity{
		ChannelRoomID:   firstNonEmpty(username, chat, sender),
		ChannelRoomType: "direct",
		DisplayName:     firstNonEmpty(chat, username, sender),
	}
}

func isGroupMessage(msg wxMessage) bool {
	chatType := strings.ToLower(strings.TrimSpace(msg.ChatType))
	return msg.IsGroup || chatType == "group" || strings.HasSuffix(strings.TrimSpace(msg.Username), "@chatroom")
}

func (a *adapter) createMessage(ctx context.Context, msg wxMessage) error {
	roomID, err := a.ensureRoom(ctx, msg)
	if err != nil {
		return err
	}
	messageTime := time.Unix(msg.Timestamp, 0).UTC()
	if msg.Timestamp <= 0 {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", msg.Time, time.Local)
		if err == nil {
			messageTime = parsed.UTC()
		} else {
			messageTime = time.Now().UTC()
		}
	}
	payloadType := normalizeWechatType(msg.Type)
	payload := map[string]any{
		"type":              payloadType,
		"text":              displayText(msg),
		"raw_text":          msg.Content,
		"wechat_type":       msg.Type,
		"wechat_chat":       msg.Chat,
		"wechat_chat_type":  msg.ChatType,
		"wechat_username":   msg.Username,
		"wechat_local_id":   msg.LocalID,
		"wechat_timestamp":  msg.Timestamp,
		"wechat_time_label": msg.Time,
	}
	reqBody := map[string]any{
		"room_id":                roomID,
		"source_message_id":      sourceMessageID(msg),
		"source":                 defaultWechatChannel,
		"sender_id":              senderID(msg),
		"sender_name":            msg.Sender,
		"message_time":           messageTime.Format(time.RFC3339),
		"payload":                payload,
		"suppress_agent_trigger": a.shouldSuppressAgentTrigger(msg),
	}
	var ignored map[string]any
	if err := a.postJSON(ctx, "/api/messages", reqBody, &ignored); err != nil {
		return fmt.Errorf("create wechat message %s: %w", sourceMessageID(msg), err)
	}
	return nil
}

func (a *adapter) postJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ClawmanBaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.ClawmanToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(respData)))
	}
	if out != nil && len(respData) > 0 {
		if err := json.Unmarshal(respData, out); err != nil {
			return err
		}
	}
	return nil
}

func normalizeWechatType(value string) string {
	switch strings.TrimSpace(value) {
	case "文本":
		return "text"
	case "图片":
		return "image"
	case "语音":
		return "voice"
	case "视频":
		return "video"
	case "表情":
		return "sticker"
	case "链接/文件":
		return "link_or_file"
	case "系统":
		return "system"
	default:
		return "unknown"
	}
}

func (a *adapter) shouldSuppressAgentTrigger(msg wxMessage) bool {
	if a.isSelfSender(msg) {
		return true
	}
	switch normalizeWechatType(msg.Type) {
	case "text", "image":
		return false
	default:
		return true
	}
}

func (a *adapter) isSelfSender(msg wxMessage) bool {
	sender := strings.TrimSpace(msg.Sender)
	if sender == "" {
		return true
	}
	return a.cfg.SelfSenders[sender]
}

func sourceMessageID(msg wxMessage) string {
	if msg.LocalID > 0 {
		return fmt.Sprintf("wechat:%s:%d", strings.TrimSpace(msg.Username), msg.LocalID)
	}
	return fmt.Sprintf("wechat:%s:%d:%s", strings.TrimSpace(msg.Username), msg.Timestamp, sourceFingerprint(msg))
}

func senderID(msg wxMessage) string {
	sender := strings.TrimSpace(msg.Sender)
	if sender == "" {
		return "self"
	}
	return sender
}

func sortMessages(messages []wxMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Timestamp != messages[j].Timestamp {
			return messages[i].Timestamp < messages[j].Timestamp
		}
		if messages[i].LocalID != messages[j].LocalID {
			return messages[i].LocalID < messages[j].LocalID
		}
		return sourceMessageID(messages[i]) < sourceMessageID(messages[j])
	})
}

func normalizeWXMessage(msg wxMessage) wxMessage {
	if msg.LocalID == 0 {
		msg.LocalID = embeddedLocalID(msg.Content)
	}
	return msg
}

func displayText(msg wxMessage) string {
	content := strings.TrimSpace(msg.Content)
	switch normalizeWechatType(msg.Type) {
	case "image":
		if content == "" || strings.HasPrefix(content, "[图片]") {
			return "[图片]"
		}
	case "sticker":
		if content == "" {
			return "[表情]"
		}
	}
	return content
}

func embeddedLocalID(content string) int64 {
	const marker = "local_id="
	index := strings.Index(content, marker)
	if index < 0 {
		return 0
	}
	start := index + len(marker)
	end := start
	for end < len(content) && content[end] >= '0' && content[end] <= '9' {
		end++
	}
	if end == start {
		return 0
	}
	id, err := strconv.ParseInt(content[start:end], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func sourceFingerprint(msg wxMessage) string {
	hash := sha1.Sum([]byte(strings.Join([]string{
		strings.TrimSpace(msg.Sender),
		strings.TrimSpace(msg.Type),
		strings.TrimSpace(msg.Time),
		strings.TrimSpace(msg.Content),
	}, "\x00")))
	return hex.EncodeToString(hash[:])[:16]
}

func envOrDefault(key string, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}

func parseBoolEnv(key string) bool {
	value := strings.TrimSpace(os.Getenv(key))
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func parseSelfSendersEnv() map[string]bool {
	raw := os.Getenv("WECHAT_SELF_SENDERS")
	if strings.TrimSpace(raw) == "" {
		raw = "私云虾虾"
	}
	return parseNameSet(raw)
}

func parseNameSetEnv(key string, def string) map[string]bool {
	return parseNameSet(envOrDefault(key, def))
}

func parseNameSet(raw string) map[string]bool {
	values := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value != "" {
			values[value] = true
		}
	}
	return values
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
