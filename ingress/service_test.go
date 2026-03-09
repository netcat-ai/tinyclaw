package ingress

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fishioon/tinyclaw/schema"
	"github.com/redis/go-redis/v9"
)

// mockWeComClient is a mock WeCom client for testing
type mockWeComClient struct {
	messages []Message
	seq      int64
}

func (m *mockWeComClient) GetMessages(seq int64, limit int) ([]Message, int64, error) {
	if seq >= int64(len(m.messages)) {
		return nil, seq, nil
	}
	end := seq + int64(limit)
	if end > int64(len(m.messages)) {
		end = int64(len(m.messages))
	}
	return m.messages[seq:end], end, nil
}

func TestSessionKeyFor(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "group message uses roomid",
			msg:  Message{From: "user1", RoomID: "room123"},
			want: "room123",
		},
		{
			name: "direct message uses from",
			msg:  Message{From: "user1", RoomID: ""},
			want: "user1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionKeyFor(tt.msg)
			if got != tt.want {
				t.Errorf("sessionKeyFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServicePublish(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx := context.Background()

	// Skip if Redis not available
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer rdb.FlushDB(ctx)

	mock := &mockWeComClient{
		messages: []Message{
			{MsgID: "msg1", From: "user1", RoomID: "room1", MsgType: "text", Content: "hello", MsgTime: time.Now().UnixMilli()},
			{MsgID: "msg2", From: "user2", RoomID: "", MsgType: "text", Content: "world", MsgTime: time.Now().UnixMilli()},
		},
	}

	svc := NewService(mock, rdb, 0)
	if err := svc.poll(ctx); err != nil {
		t.Fatalf("poll() error: %v", err)
	}

	// Check room1 stream
	streamKey := schema.StreamKey("room1")
	msgs, err := rdb.XRange(ctx, streamKey, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in room1 stream, got %d", len(msgs))
	}

	var event schema.Event
	if err := json.Unmarshal([]byte(msgs[0].Values["event"].(string)), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.SessionID != "room1" {
		t.Errorf("expected session_id=room1, got %s", event.SessionID)
	}
	if event.Payload != "hello" {
		t.Errorf("expected payload=hello, got %s", event.Payload)
	}

	// Check user2 direct message stream
	dmKey := schema.StreamKey("user2")
	dmMsgs, err := rdb.XRange(ctx, dmKey, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange error: %v", err)
	}
	if len(dmMsgs) != 1 {
		t.Fatalf("expected 1 message in user2 stream, got %d", len(dmMsgs))
	}
}

func TestServiceSeqAdvance(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx := context.Background()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer rdb.FlushDB(ctx)

	mock := &mockWeComClient{
		messages: []Message{
			{MsgID: "msg1", From: "u1", MsgType: "text", Content: "a", MsgTime: 1},
			{MsgID: "msg2", From: "u1", MsgType: "text", Content: "b", MsgTime: 2},
			{MsgID: "msg3", From: "u1", MsgType: "text", Content: "c", MsgTime: 3},
		},
	}

	svc := NewService(mock, rdb, 0)

	// First poll: fetch all 3
	if err := svc.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.seq != 3 {
		t.Errorf("expected seq=3 after first poll, got %d", svc.seq)
	}

	// Second poll: no new messages
	if err := svc.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.seq != 3 {
		t.Errorf("expected seq=3 after second poll, got %d", svc.seq)
	}
}
