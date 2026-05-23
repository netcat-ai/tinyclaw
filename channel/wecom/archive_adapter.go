package wecom

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"tinyclaw/channel/wecom/finance"
	"tinyclaw/internal/core"
)

const (
	archiveCursorChannel = "wecom_archive"
	defaultWeComChannel  = "wecom"
)

type ArchiveConfig struct {
	CorpID        string
	CorpSecret    string
	ContactSecret string
	RSAPrivateKey string
	BotID         string
	Proxy         string
	ProxyPassword string
	PollInterval  time.Duration
	PollLimit     int64
	SDKTimeout    int
	StartSeq      int64
}

type InboundStore interface {
	RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error)
	CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
}

type MessageIngestor interface {
	IngestMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
}

type ArchiveAdapter struct {
	db            *sql.DB
	store         InboundStore
	messages      MessageIngestor
	cfg           ArchiveConfig
	archiveClient *Client
	contactClient *Client
}

type directMessageIngestor struct {
	store InboundStore
}

func (i directMessageIngestor) IngestMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	return i.store.CreateMessage(ctx, input)
}

type archiveMessage struct {
	MsgID   string   `json:"msgid"`
	Action  string   `json:"action"`
	From    string   `json:"from"`
	ToList  []string `json:"tolist"`
	RoomID  string   `json:"roomid"`
	MsgTime int64    `json:"msgtime"`
	MsgType string   `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

func NewArchiveAdapter(db *sql.DB, store InboundStore, cfg ArchiveConfig) *ArchiveAdapter {
	cfg = cfg.withDefaults()
	contactSecret := strings.TrimSpace(cfg.ContactSecret)
	if contactSecret == "" {
		contactSecret = cfg.CorpSecret
	}
	return &ArchiveAdapter{
		db:            db,
		store:         store,
		messages:      directMessageIngestor{store: store},
		cfg:           cfg,
		archiveClient: NewClient(cfg.CorpID, cfg.CorpSecret),
		contactClient: NewClient(cfg.CorpID, contactSecret),
	}
}

func (a *ArchiveAdapter) SetMessageIngestor(messages MessageIngestor) {
	a.messages = messages
}

func (c ArchiveConfig) withDefaults() ArchiveConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 3 * time.Second
	}
	if c.PollLimit <= 0 {
		c.PollLimit = 100
	}
	if c.SDKTimeout <= 0 {
		c.SDKTimeout = 30
	}
	return c
}

func (a *ArchiveAdapter) Run(ctx context.Context) error {
	if err := a.validate(); err != nil {
		return err
	}
	if err := ensureCursorSchema(ctx, a.db); err != nil {
		return err
	}
	sdk, err := finance.NewSDK(a.cfg.CorpID, a.cfg.CorpSecret, a.cfg.RSAPrivateKey, a.cfg.Proxy, a.cfg.ProxyPassword, a.cfg.SDKTimeout)
	if err != nil {
		return fmt.Errorf("init wecom finance sdk: %w", err)
	}
	defer sdk.Free()

	slog.Info("wecom archive adapter starting", "interval", a.cfg.PollInterval, "limit", a.cfg.PollLimit)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		processed, err := a.pollOnce(ctx, sdk)
		if err != nil {
			slog.Error("wecom archive poll failed", "err", err)
		}
		if processed > 0 {
			continue
		}
		timer := time.NewTimer(a.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (a *ArchiveAdapter) validate() error {
	switch {
	case a.db == nil:
		return fmt.Errorf("database is required")
	case a.store == nil:
		return fmt.Errorf("core store is required")
	case strings.TrimSpace(a.cfg.CorpID) == "":
		return fmt.Errorf("WECOM_CORP_ID is required")
	case strings.TrimSpace(a.cfg.CorpSecret) == "":
		return fmt.Errorf("WECOM_CORP_SECRET is required")
	case strings.TrimSpace(a.cfg.RSAPrivateKey) == "":
		return fmt.Errorf("WECOM_RSA_PRIVATE_KEY is required")
	}
	return nil
}

func (a *ArchiveAdapter) pollOnce(ctx context.Context, sdk *finance.SDK) (int, error) {
	seq, err := loadCursor(ctx, a.db, archiveCursorChannel, a.cfg.StartSeq)
	if err != nil {
		return 0, err
	}
	items, err := sdk.GetChatData(seq, a.cfg.PollLimit)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := a.ingestChatData(ctx, sdk, item); err != nil {
			return 0, err
		}
		if err := saveCursor(ctx, a.db, archiveCursorChannel, item.Seq); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func (a *ArchiveAdapter) ingestChatData(ctx context.Context, sdk *finance.SDK, item finance.ChatData) error {
	plain, err := sdk.DecryptData(&item)
	if err != nil {
		return fmt.Errorf("decrypt wecom message seq %d: %w", item.Seq, err)
	}
	var msg archiveMessage
	if err := json.Unmarshal(plain, &msg); err != nil {
		return fmt.Errorf("decode wecom message seq %d: %w", item.Seq, err)
	}
	return a.ingestArchiveMessage(ctx, item.Seq, msg, plain)
}

func (a *ArchiveAdapter) ingestArchiveMessage(ctx context.Context, seq int64, msg archiveMessage, plain json.RawMessage) error {
	roomInput, ok := a.toRegisterRoomInput(ctx, msg)
	if !ok {
		slog.Warn("skip wecom message because room profile is unresolved", "seq", seq, "msgid", msg.MsgID)
		return nil
	}
	roomResult, err := a.store.RegisterRoom(ctx, roomInput)
	if err != nil {
		return fmt.Errorf("register wecom room seq %d: %w", seq, err)
	}
	messageInput := a.toCreateMessageInput(ctx, seq, msg, plain, roomResult.Room.ID)
	_, err = a.messages.IngestMessage(ctx, messageInput)
	if err != nil {
		return fmt.Errorf("create wecom message seq %d: %w", seq, err)
	}
	return nil
}

func (a *ArchiveAdapter) toRegisterRoomInput(ctx context.Context, msg archiveMessage) (core.RegisterRoomInput, bool) {
	roomType := core.RoomChatTypeDirect
	channelRoomID := directRoomID(msg, a.cfg.BotID)
	if strings.TrimSpace(msg.RoomID) != "" {
		roomType = core.RoomChatTypeGroup
		channelRoomID = strings.TrimSpace(msg.RoomID)
	}
	displayName := a.resolveRoomDisplayName(ctx, roomType, channelRoomID)
	if displayName == "" {
		return core.RegisterRoomInput{}, false
	}
	return core.RegisterRoomInput{
		Channel:         defaultWeComChannel,
		ChannelRoomID:   channelRoomID,
		ChannelRoomType: roomType,
		DisplayName:     displayName,
		OutboundAlias:   displayName,
		AgentKey:        core.DefaultAgentKey,
		AgentEnabled:    true,
	}, true
}

func (a *ArchiveAdapter) toCreateMessageInput(ctx context.Context, seq int64, msg archiveMessage, raw json.RawMessage, roomID int64) core.CreateMessageInput {
	skipped := strings.TrimSpace(msg.Action) != "send" || strings.TrimSpace(msg.MsgType) != "text" || strings.TrimSpace(msg.From) == strings.TrimSpace(a.cfg.BotID)
	payload := map[string]any{
		"type":        msg.MsgType,
		"wecom_seq":   seq,
		"wecom_msgid": strings.TrimSpace(msg.MsgID),
	}
	if strings.TrimSpace(msg.MsgType) == "text" {
		payload["type"] = "text"
		payload["text"] = msg.Text.Content
	}
	if skipped {
		payload["raw"] = json.RawMessage(raw)
	}

	return core.CreateMessageInput{
		RoomID:          roomID,
		SourceMessageID: sourceMessageID(seq, msg.MsgID),
		SenderID:        strings.TrimSpace(msg.From),
		SenderName:      a.resolveSenderName(ctx, strings.TrimSpace(msg.From)),
		MessageTime:     archiveMessageTime(msg.MsgTime),
		Payload:         mustJSON(payload),
		Skipped:         skipped,
	}
}

func (a *ArchiveAdapter) resolveRoomDisplayName(ctx context.Context, roomType string, channelRoomID string) string {
	channelRoomID = strings.TrimSpace(channelRoomID)
	if channelRoomID == "" {
		return ""
	}
	if roomType == core.RoomChatTypeGroup {
		if a.archiveClient == nil {
			return ""
		}
		chat, err := a.archiveClient.GetArchiveGroupChat(ctx, channelRoomID)
		if err == nil {
			return strings.TrimSpace(chat.Name)
		}
		if name := a.resolveExternalGroupDisplayName(ctx, channelRoomID, err); name != "" {
			return name
		}
		if err != nil {
			slog.Warn("resolve wecom group name failed", "room_id", channelRoomID, "err", err)
			return ""
		}
	}
	if a.contactClient == nil {
		return ""
	}
	if contact, err := a.contactClient.GetExternalContact(ctx, channelRoomID); err == nil && strings.TrimSpace(contact.Name) != "" {
		return strings.TrimSpace(contact.Name)
	}
	if user, err := a.contactClient.GetUser(ctx, channelRoomID); err == nil && strings.TrimSpace(user.Name) != "" {
		return strings.TrimSpace(user.Name)
	}
	slog.Warn("resolve wecom direct room name failed", "room_id", channelRoomID)
	return ""
}

func (a *ArchiveAdapter) resolveExternalGroupDisplayName(ctx context.Context, channelRoomID string, archiveErr error) string {
	apiErr, ok := archiveErr.(*APIError)
	if !ok || apiErr.Code != 301059 || a.contactClient == nil {
		return ""
	}
	chat, err := a.contactClient.GetGroupChat(ctx, channelRoomID)
	if err != nil {
		slog.Warn("resolve wecom external group name failed", "room_id", channelRoomID, "err", err)
		return ""
	}
	return strings.TrimSpace(chat.Name)
}

func (a *ArchiveAdapter) resolveSenderName(ctx context.Context, senderID string) string {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" || a.contactClient == nil {
		return ""
	}
	if contact, err := a.contactClient.GetExternalContact(ctx, senderID); err == nil && strings.TrimSpace(contact.Name) != "" {
		return strings.TrimSpace(contact.Name)
	}
	if user, err := a.contactClient.GetUser(ctx, senderID); err == nil && strings.TrimSpace(user.Name) != "" {
		return strings.TrimSpace(user.Name)
	}
	return ""
}

func directRoomID(msg archiveMessage, botID string) string {
	from := strings.TrimSpace(msg.From)
	if from != "" && from != strings.TrimSpace(botID) {
		return from
	}
	for _, to := range msg.ToList {
		to = strings.TrimSpace(to)
		if to != "" && to != strings.TrimSpace(botID) {
			return to
		}
	}
	if from != "" {
		return from
	}
	return "unknown"
}

func sourceMessageID(seq int64, msgID string) string {
	msgID = strings.TrimSpace(msgID)
	if msgID != "" {
		return msgID
	}
	return fmt.Sprintf("wecom-seq-%d", seq)
}

func archiveMessageTime(value int64) time.Time {
	if value <= 0 {
		return time.Now().UTC()
	}
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func ensureCursorSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS channel_adapter_cursors (
			channel TEXT PRIMARY KEY,
			cursor_value BIGINT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure channel adapter cursor schema: %w", err)
	}
	return nil
}

func loadCursor(ctx context.Context, db *sql.DB, channel string, fallback int64) (int64, error) {
	var value int64
	err := db.QueryRowContext(ctx, `SELECT cursor_value FROM channel_adapter_cursors WHERE channel = $1`, channel).Scan(&value)
	if err == nil {
		return value, nil
	}
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	return 0, fmt.Errorf("load channel adapter cursor: %w", err)
}

func saveCursor(ctx context.Context, db *sql.DB, channel string, value int64) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO channel_adapter_cursors (channel, cursor_value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (channel) DO UPDATE
		SET cursor_value = EXCLUDED.cursor_value,
			updated_at = EXCLUDED.updated_at
	`, channel, value)
	if err != nil {
		return fmt.Errorf("save channel adapter cursor: %w", err)
	}
	return nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
