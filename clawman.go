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
	redis      *redis.Client
	sdk        *finance.SDK
	contactAPI *wecom.Client
	archiveAPI *wecom.Client
	orch       *sandbox.Orchestrator
	egress     *EgressConsumer
	seq        uint64
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

	var contactAPI *wecom.Client
	if cfg.WeComContactSecret != "" {
		contactAPI = wecom.NewClient(cfg.WeComCorpID, cfg.WeComContactSecret)
	}
	archiveAPI := wecom.NewClient(cfg.WeComCorpID, cfg.WeComCorpSecret)

	return &Clawman{
		cfg:        cfg,
		redis:      rdb,
		sdk:        sdk,
		contactAPI: contactAPI,
		archiveAPI: archiveAPI,
		orch:       orch,
		egress:     egress,
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
		if !r.primeSenderIdentity(ctx, &msg) {
			continue
		}

		roomID := msg.RoomID
		if msg.RoomID == "" {
			roomID = msg.From
		}

		if r.orch != nil {
			r.orch.Ensure(ctx, roomID)
		}

		stream := "stream:i:" + roomID
		if err := r.redis.XAdd(ctx, &redis.XAddArgs{
			Stream: stream,
			Values: map[string]any{
				"msgid": msg.MsgID,
				"kind":  "wecom",
				"raw":   msg.RawContent,
			},
		}).Err(); err != nil {
			return seq, fmt.Errorf("xadd %s failed: %w", stream, err)
		}
		if err := r.redis.Set(ctx, r.cfg.WeComSeqKey, chatData.Seq, 0).Err(); err != nil {
			return seq, fmt.Errorf("set seq in redis: %w", err)
		}
		seq = chatData.Seq

		// Register egress stream for this room
		if r.egress != nil {
			r.egress.RegisterRoom(ctx, roomID)
			r.cacheTarget(ctx, &msg, roomID)
		}
	}

	return seq, nil
}

func (r *Clawman) primeSenderIdentity(ctx context.Context, msg *WeComMessage) bool {
	failKey := primeSenderFailCachePrefix + msg.From
	if exists, err := r.redis.Exists(ctx, failKey).Result(); err == nil && exists > 0 {
		return false
	}
	if _, err := r.Resolve(ctx, msg.From); err != nil {
		r.redis.Set(ctx, failKey, err.Error(), primeSenderFailTTL)
		slog.Error("resolve sender on receive failed", "from", msg.From, "msgid", msg.MsgID, "err", err)
		return false
	}
	return true
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
		// Group chat — resolve room metadata from customer-group or internal-group APIs.
		group, err := r.ResolveGroup(ctx, msg.RoomID)
		if err != nil {
			slog.Warn("cache target: resolve group failed", "room_id", msg.RoomID, "err", err)
			return
		}
		target = group.Name
	} else {
		// Direct message — resolve sender from external-contact or internal-user API.
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
	r.redis.Set(ctx, key, target, detailCacheTTL)
	slog.Info("cached target", "room_id", roomID, "target", target)
}

// Resolve resolves a WeCom sender ID to an Identity.
// Direct messages use sender identity to decide between external contact and internal user APIs.
func (r *Clawman) Resolve(ctx context.Context, id string) (*Identity, error) {
	cacheKey := internalUserCachePrefix + id
	if isExternalUserID(id) {
		cacheKey = externalContactCachePrefix + id
	}

	if cached, err := r.redis.Get(ctx, cacheKey).Bytes(); err == nil {
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
		r.redis.Set(ctx, cacheKey, data, detailCacheTTL)
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
func (r *Clawman) ResolveGroup(ctx context.Context, roomID string) (*GroupDetail, error) {
	if cached, err := r.redis.Get(ctx, groupDetailCachePrefix+roomID).Bytes(); err == nil {
		detail := &GroupDetail{}
		if json.Unmarshal(cached, detail) == nil {
			return detail, nil
		}
	}
	detail, err := r.resolveGroup(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(detail); err == nil {
		r.redis.Set(ctx, groupDetailCachePrefix+roomID, data, detailCacheTTL)
	}
	return detail, nil
}

func (r *Clawman) resolveGroup(ctx context.Context, roomID string) (*GroupDetail, error) {
	if r.archiveAPI != nil {
		group, err := r.archiveAPI.GetArchiveGroupChat(ctx, roomID)
		if err == nil {
			return &GroupDetail{
				ChatID: group.ChatID,
				Name:   group.Name,
				Type:   "internal_group",
			}, nil
		}
	}

	if r.contactAPI == nil {
		return nil, fmt.Errorf("contact api not configured")
	}
	group, err := r.contactAPI.GetGroupChat(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("resolve group %s: %w", roomID, err)
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
