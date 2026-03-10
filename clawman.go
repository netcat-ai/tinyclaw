package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"tinyclaw/wecom/finance"
)

type Clawman struct {
	cfg   Config
	redis *redis.Client
	sdk   *finance.SDK
	seq   uint64
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

func NewClawman(cfg Config, rdb *redis.Client) (*Clawman, error) {
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

	r := &Clawman{
		cfg:   cfg,
		redis: rdb,
		sdk:   sdk,
	}

	return r, nil
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
			log.Printf("pull and dispatch failed: %v", err)
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
		log.Printf("pulled=0 dispatched=0 seq=%d", seq)
		return seq, nil
	}

	for _, chatData := range chatDataList {
		if ctx.Err() != nil {
			return seq, ctx.Err()
		}
		raw, err := r.sdk.DecryptData(&chatData)
		if err != nil {
			log.Printf("skip message decrypt failed: seq=%d msgid=%s err=%v", chatData.Seq, chatData.MsgID, err)
			continue
		}

		var msg WeComMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("skip invalid message json: seq=%d msgid=%s err=%v", chatData.Seq, chatData.MsgID, err)
			continue
		}
		msg.RawContent = string(raw)
		if msg.From == "" || len(msg.ToList) == 0 {
			log.Printf("skip invalid message without from/tolist: seq=%d msgid=%s", chatData.Seq, chatData.MsgID)
			continue
		}

		stream := streamKey(r.cfg.StreamPrefix, &msg)
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
	}

	return seq, nil
}

func streamValues(msg WeComMessage) map[string]any {
	return map[string]any{
		"msgid": msg.MsgID,
		"raw":   msg.RawContent,
	}
}

func streamKey(prefix string, msg *WeComMessage) string {
	roomID := msg.RoomID
	if msg.RoomID == "" {
		from, to := msg.From, msg.ToList[0]
		if from > to {
			from, to = to, from
		}
		roomID = from + "-" + to
	}
	return prefix + ":" + roomID
}
