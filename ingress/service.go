package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/fishioon/tinyclaw/schema"
	"github.com/redis/go-redis/v9"
)

// WeComClient defines the interface for fetching WeCom messages
type WeComClient interface {
	GetMessages(seq int64, limit int) ([]Message, int64, error)
}

// Message represents a WeCom session archive message
type Message struct {
	MsgID   string   `json:"msgid"`
	From    string   `json:"from"`
	ToList  []string `json:"tolist"`
	RoomID  string   `json:"roomid"`
	MsgTime int64    `json:"msgtime"`
	MsgType string   `json:"msgtype"`
	Content string   `json:"content"`
}

// Service pulls WeCom session archive messages and writes to Redis streams
type Service struct {
	wecom  WeComClient
	redis  *redis.Client
	seq    int64
	ticker *time.Ticker
}

func NewService(wecom WeComClient, rdb *redis.Client, startSeq int64) *Service {
	return &Service{
		wecom: wecom,
		redis: rdb,
		seq:   startSeq,
	}
}

// Run starts the ingress polling loop
func (s *Service) Run(ctx context.Context, interval time.Duration) error {
	s.ticker = time.NewTicker(interval)
	defer s.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ticker.C:
			if err := s.poll(ctx); err != nil {
				log.Printf("ingress poll error: %v", err)
			}
		}
	}
}

func (s *Service) poll(ctx context.Context) error {
	msgs, nextSeq, err := s.wecom.GetMessages(s.seq, 100)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}
	for _, msg := range msgs {
		if err := s.publish(ctx, msg); err != nil {
			return fmt.Errorf("publish msg %s: %w", msg.MsgID, err)
		}
	}
	s.seq = nextSeq
	return nil
}

func (s *Service) publish(ctx context.Context, msg Message) error {
	sessionKey := sessionKeyFor(msg)
	event := schema.Event{
		Type:      schema.EventTypeMessage,
		SessionID: sessionKey,
		Payload:   msg.Content,
		Meta: map[string]string{
			"msg_id":   msg.MsgID,
			"from":     msg.From,
			"msg_type": msg.MsgType,
		},
		CreatedAt: msg.MsgTime,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}

	streamKey := schema.StreamKey(sessionKey)
	return s.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"event": string(payload),
		},
	}).Err()
}

// sessionKeyFor returns the session key for a message.
// Group messages use roomid, direct messages use sender id.
func sessionKeyFor(msg Message) string {
	if msg.RoomID != "" {
		return msg.RoomID
	}
	return msg.From
}
