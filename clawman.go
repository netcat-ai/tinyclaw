package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"tinyclaw/sandbox"
	"tinyclaw/wecom"
	"tinyclaw/wecom/finance"
)

const (
	cachePrefix      = "wecom:id2name:"
	groupOwnerPrefix = "wecom:group:owner:"
	cacheTTL         = 24 * time.Hour
)

// Identity represents a resolved WeCom user identity.
type Identity struct {
	UserID   string `json:"userid"`
	Name     string `json:"name"`
	Type     string `json:"type"`      // "employee" | "external" | "guest"
	CorpName string `json:"corp_name"` // 外部联系人所属企业
}

type Clawman struct {
	cfg    Config
	redis  *redis.Client
	sdk    *finance.SDK
	wecom  *wecom.Client
	orch   *sandbox.Orchestrator
	egress *EgressConsumer
	seq    uint64
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

func NewClawman(cfg Config, rdb *redis.Client, orch *sandbox.Orchestrator, egress *EgressConsumer) (*Clawman, error) {
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

	var wc *wecom.Client
	if cfg.WeComContactSecret != "" {
		wc = wecom.NewClient(cfg.WeComCorpID, cfg.WeComContactSecret)
	}

	return &Clawman{
		cfg:    cfg,
		redis:  rdb,
		sdk:    sdk,
		wecom:  wc,
		orch:   orch,
		egress: egress,
	}, nil
}

func (r *Clawman) Close() {
	if r.sdk != nil {
		r.sdk.Free()
	}
}

func (r *Clawman) Run(ctx context.Context) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	seq, err := r.redis.Get(ctx, r.cfg.WeComSeqKey).Int64()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get seq from redis: %w", err)
	}
	for {
		seq, err = r.pullAndDispatch(ctx, seq, 100)
		if err != nil {
			slog.Error("pull and dispatch failed", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Clawman) pullAndDispatch(ctx context.Context, seq, limit int64) (int64, error) {
	chatDataList, err := r.sdk.GetChatData(seq, limit)
	if err != nil {
		return seq, fmt.Errorf("sdk get chat data failed: seq=%d limit=%d err=%w", seq, limit, err)
	}

	if len(chatDataList) == 0 {
		slog.Info("pull completed", "pulled", 0, "dispatched", 0, "seq", seq)
		return seq, nil
	}

	for _, chatData := range chatDataList {
		if ctx.Err() != nil {
			return seq, ctx.Err()
		}
		raw, err := r.sdk.DecryptData(&chatData)
		if err != nil {
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
		// 私聊中忽略 bot 自己发出的消息
		if msg.RoomID == "" && r.cfg.WeComBotID != "" && msg.From == r.cfg.WeComBotID {
			continue
		}

		roomID := msg.RoomID
		if msg.RoomID == "" {
			roomID = msg.From
		}

		stream := "stream:i:" + roomID
		if err := r.redis.XAdd(ctx, &redis.XAddArgs{
			Stream: stream,
			Values: streamValues(msg),
		}).Err(); err != nil {
			return seq, fmt.Errorf("xadd %s failed: %w", stream, err)
		}
		if err := r.redis.Set(ctx, r.cfg.WeComSeqKey, chatData.Seq, 0).Err(); err != nil {
			return seq, fmt.Errorf("set seq in redis: %w", err)
		}
		seq = chatData.Seq

		if r.orch != nil {
			ct := chatTypeFromMsg(&msg)
			r.orch.Ensure(ctx, roomID, r.cfg.WeComCorpID, ct)
		}

		// Register egress stream for this room
		if r.egress != nil {
			r.egress.RegisterRoom(ctx, roomID)
		}

		// Cache room target name for egress lookup
		r.cacheTarget(ctx, &msg, roomID)
	}

	return seq, nil
}

func streamValues(msg WeComMessage) map[string]any {
	return map[string]any{
		"msgid":   msg.MsgID,
		"from":    msg.From,
		"msgtime": msg.MsgTime,
		"msgtype": msg.MsgType,
		"text":    extractText(msg.RawContent, msg.MsgType),
		"raw":     msg.RawContent,
	}
}

// extractText pulls the human-readable text out of the raw WeCom message JSON.
// Falls back to the raw content string so the agent always has something to work with.
func extractText(raw, msgType string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	switch msgType {
	case "text":
		var t struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(m["text"], &t); err == nil && t.Content != "" {
			return t.Content
		}
	case "image", "voice", "video", "file":
		return "[" + msgType + "]"
	}
	return raw
}

// cacheTarget resolves and caches the display name for a room/user so egress
// can look it up later. Skips if already cached. Errors are logged, never returned.
func (r *Clawman) cacheTarget(ctx context.Context, msg *WeComMessage, roomID string) {
	key := targetPrefix + roomID
	if exists, _ := r.redis.Exists(ctx, key).Result(); exists > 0 {
		return
	}

	var target string
	if msg.RoomID != "" {
		// Group chat — resolve group name
		if r.wecom == nil {
			return
		}
		chat, err := r.wecom.GetGroupChat(ctx, msg.RoomID)
		if err != nil {
			slog.Warn("cache target: get group chat failed", "room_id", msg.RoomID, "err", err)
			return
		}
		target = chat.Name
	} else {
		// DM — resolve sender name
		ident, err := r.Resolve(ctx, msg.From)
		if err != nil {
			slog.Warn("cache target: resolve sender failed", "from", msg.From, "err", err)
			return
		}
		target = ident.Name
	}

	if target == "" {
		return
	}
	r.redis.Set(ctx, key, target, cacheTTL)
	slog.Info("cached target", "room_id", roomID, "target", target)
}

// chatTypeFromMsg returns "group" for group chats, "dm" for direct messages.
func chatTypeFromMsg(msg *WeComMessage) string {
	if msg.RoomID != "" {
		return "group"
	}
	return "dm"
}

// Resolve resolves a WeCom ID to an Identity.
// Routing by prefix: wm/wo → external contact, others → employee.
// Falls back to guest on any error.
func (r *Clawman) Resolve(ctx context.Context, id string) (*Identity, error) {
	if cached, err := r.redis.Get(ctx, cachePrefix+id).Bytes(); err == nil {
		ident := &Identity{}
		if json.Unmarshal(cached, ident) == nil {
			return ident, nil
		}
	}

	ident := r.resolveIdentity(ctx, id)

	if data, err := json.Marshal(ident); err == nil {
		r.redis.Set(ctx, cachePrefix+id, data, cacheTTL)
	}
	return ident, nil
}

func (r *Clawman) resolveIdentity(ctx context.Context, id string) *Identity {
	if strings.HasPrefix(id, "wm") || strings.HasPrefix(id, "wo") {
		return r.resolveExternal(ctx, id)
	}
	return &Identity{UserID: id, Name: id, Type: "employee"}
}

func (r *Clawman) resolveExternal(ctx context.Context, id string) *Identity {
	if r.wecom == nil {
		return &Identity{UserID: id, Name: id, Type: "guest"}
	}
	contact, err := r.wecom.GetExternalContact(ctx, id)
	if err != nil {
		return &Identity{UserID: id, Name: id, Type: "guest"}
	}
	return &Identity{
		UserID:   id,
		Name:     contact.Name,
		Type:     "external",
		CorpName: contact.CorpName,
	}
}

// ResolveGroupOwner returns the owner userid of a group chat.
func (r *Clawman) ResolveGroupOwner(ctx context.Context, roomID string) (string, error) {
	if owner, err := r.redis.Get(ctx, groupOwnerPrefix+roomID).Result(); err == nil {
		return owner, nil
	}
	if r.wecom == nil {
		return "", fmt.Errorf("wecom client not configured")
	}
	chat, err := r.wecom.GetGroupChat(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("get group chat %s: %w", roomID, err)
	}
	r.redis.Set(ctx, groupOwnerPrefix+roomID, chat.Owner, cacheTTL)
	return chat.Owner, nil
}
