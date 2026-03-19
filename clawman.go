package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
		cfg:        cfg,
		store:      store,
		sdk:        sdk,
		contactAPI: contactAPI,
		archiveAPI: archiveAPI,
		orch:       orch,
		egress:     egress,
		cache:      newTTLCache(),
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

		text, err := extractWeComMessageText(msg.RawContent)
		if err != nil {
			slog.Warn("skip unsupported wecom message payload", "msgid", msg.MsgID, "room_id", roomID, "err", err)
			continue
		}
		targetName, senderName, err := r.resolveRoutingTarget(ctx, &msg, roomID)
		if err != nil {
			return seq, fmt.Errorf("resolve target for room %s: %w", roomID, err)
		}

		if r.orch == nil {
			return seq, fmt.Errorf("sandbox integration not configured")
		}

		reply, err := r.orch.InvokeAgent(ctx, roomID, sandbox.AgentRequest{
			Query:    text,
			MsgID:    msg.MsgID,
			RoomID:   roomID,
			TenantID: r.cfg.WeComCorpID,
			ChatType: chatTypeForRoom(msg.RoomID),
		})
		if err != nil {
			return seq, fmt.Errorf("invoke sandbox for room %s: %w", roomID, err)
		}

		stored, err := r.store.StoreConversation(ctx,
			InboundMessageRecord{
				ID:            "in:" + msg.MsgID,
				TenantID:      r.cfg.WeComCorpID,
				RoomID:        roomID,
				PlatformMsgID: msg.MsgID,
				SenderID:      msg.From,
				SenderName:    senderName,
				Content:       text,
				RawPayload:    msg.RawContent,
				CreatedAt:     time.UnixMilli(msg.MsgTime),
			},
			OutboundMessageRecord{
				ID:         "out:" + msg.MsgID,
				TenantID:   r.cfg.WeComCorpID,
				RoomID:     roomID,
				Content:    reply.Stdout,
				TargetName: targetName,
				CreatedAt:  time.Now().UTC(),
			},
		)
		if err != nil {
			return seq, fmt.Errorf("store conversation for room %s: %w", roomID, err)
		}
		if !stored {
			slog.Info("skip duplicate already persisted message", "msgid", msg.MsgID, "room_id", roomID)
		}
		if err := r.store.SetCursor(ctx, wecomCursorSource, r.cfg.WeComCorpID, chatData.Seq); err != nil {
			return seq, fmt.Errorf("set cursor in postgres: %w", err)
		}
		seq = chatData.Seq
	}

	return seq, nil
}

func (r *Clawman) primeSenderIdentity(ctx context.Context, msg *WeComMessage) bool {
	failKey := primeSenderFailCachePrefix + msg.From
	if r.cache.Has(failKey) {
		return false
	}
	if _, err := r.Resolve(ctx, msg.From); err != nil {
		r.cache.Set(failKey, []byte(err.Error()), primeSenderFailTTL)
		slog.Error("resolve sender on receive failed", "from", msg.From, "msgid", msg.MsgID, "err", err)
		return false
	}
	return true
}

func (r *Clawman) resolveRoutingTarget(ctx context.Context, msg *WeComMessage, roomID string) (targetName string, senderName string, err error) {
	if msg.RoomID != "" {
		group, err := r.ResolveGroup(ctx, msg.RoomID)
		if err != nil {
			return "", "", err
		}
		sender, err := r.Resolve(ctx, msg.From)
		if err != nil {
			return group.Name, "", err
		}
		return group.Name, sender.Name, nil
	}

	ident, err := r.Resolve(ctx, msg.From)
	if err != nil {
		return "", "", err
	}
	return ident.Name, ident.Name, nil
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
	if cached, ok := r.cache.Get(groupDetailCachePrefix + roomID); ok {
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
		r.cache.Set(groupDetailCachePrefix+roomID, data, detailCacheTTL)
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

func chatTypeForRoom(roomID string) string {
	if roomID == "" {
		return "direct"
	}
	return "group"
}
