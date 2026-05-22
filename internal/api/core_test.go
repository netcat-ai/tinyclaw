package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

type fakeCoreStore struct {
	registerFn func(context.Context, core.RegisterRoomInput) (core.RegisterRoomResult, error)
	messageFn  func(context.Context, core.CreateMessageInput) (core.CreateMessageResult, error)
	listFn     func(context.Context, string, int64) ([]core.Delivery, error)
	ackFn      func(context.Context, int64) (core.Delivery, error)
	memoryFn   func(context.Context, string, core.MemorySearchInput) ([]core.MemoryItem, error)
}

func (f fakeCoreStore) RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error) {
	return f.registerFn(ctx, input)
}

func (f fakeCoreStore) CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	return f.messageFn(ctx, input)
}

func (f fakeCoreStore) ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error) {
	return f.listFn(ctx, channel, afterID)
}

func (f fakeCoreStore) AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error) {
	return f.ackFn(ctx, id)
}

func (f fakeCoreStore) SearchRoomMemoryByToken(ctx context.Context, token string, input core.MemorySearchInput) ([]core.MemoryItem, error) {
	return f.memoryFn(ctx, token, input)
}

type fakeCommandHandler struct {
	calls   int
	message core.Message
}

func (h *fakeCommandHandler) HandleMessage(_ context.Context, message core.Message) bool {
	h.calls++
	h.message = message
	return true
}

func TestHealthz(t *testing.T) {
	api := NewServer(nil, "api-secret")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("body = %q, want health payload", rec.Body.String())
	}
}

func TestHandleMessagesSuppressesAgentTriggerForDrawCommand(t *testing.T) {
	commands := &fakeCommandHandler{}
	api := NewServerWithCommandHandler(fakeCoreStore{
		messageFn: func(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
			if !input.SuppressAgentTrigger {
				t.Fatal("SuppressAgentTrigger = false, want true")
			}
			if input.Skipped {
				t.Fatal("Skipped = true, want false")
			}
			return core.CreateMessageResult{
				Message: core.Message{
					ID:              20,
					RoomID:          input.RoomID,
					SourceMessageID: input.SourceMessageID,
					SenderID:        input.SenderID,
					Payload:         input.Payload,
				},
			}, nil
		},
	}, commands, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{
		"room_id":10,
		"source_message_id":"msg-1",
		"sender_id":"alice",
		"payload":{"type":"text","text":"/draw 一朵花"}
	}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload createMessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Triggered {
		t.Fatal("triggered = true, want false")
	}
	if commands.calls != 1 || commands.message.ID != 20 {
		t.Fatalf("command calls=%d message=%+v, want one call for message 20", commands.calls, commands.message)
	}
}

func TestHandleMessagesDoesNotHandleDuplicateDrawCommand(t *testing.T) {
	commands := &fakeCommandHandler{}
	api := NewServerWithCommandHandler(fakeCoreStore{
		messageFn: func(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
			return core.CreateMessageResult{
				Message: core.Message{
					ID:              20,
					RoomID:          input.RoomID,
					SourceMessageID: input.SourceMessageID,
					SenderID:        input.SenderID,
					Payload:         input.Payload,
				},
				Duplicate: true,
			}, nil
		},
	}, commands, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{
		"room_id":10,
		"source_message_id":"msg-1",
		"sender_id":"alice",
		"payload":{"type":"text","text":"/draw 一朵花"}
	}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if commands.calls != 0 {
		t.Fatalf("command calls = %d, want 0", commands.calls)
	}
}

func TestHandleRoomsRequiresAPIToken(t *testing.T) {
	api := NewServer(fakeCoreStore{
		registerFn: func(context.Context, core.RegisterRoomInput) (core.RegisterRoomResult, error) {
			t.Fatal("RegisterRoom should not be called without auth")
			return core.RegisterRoomResult{}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleRoomsRegistersRoom(t *testing.T) {
	api := NewServer(fakeCoreStore{
		registerFn: func(_ context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error) {
			if input.Channel != "wecom" || input.ChannelRoomID != "room-1" || input.OutboundAlias != "测试 AI" {
				t.Fatalf("unexpected input: %+v", input)
			}
			return core.RegisterRoomResult{
				Room: core.Room{
					ID:              10,
					TenantID:        core.DefaultTenantID,
					Channel:         input.Channel,
					ChannelRoomID:   input.ChannelRoomID,
					ChannelRoomType: input.ChannelRoomType,
					DisplayName:     input.DisplayName,
					OutboundAlias:   input.OutboundAlias,
				},
				AgentSession: core.AgentSession{
					ID:       100,
					RoomID:   10,
					AgentKey: core.DefaultAgentKey,
					Enabled:  input.AgentEnabled,
				},
			}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(`{
		"channel":"wecom",
		"channel_room_id":"room-1",
		"channel_room_type":"group",
		"display_name":"测试 AI",
		"outbound_alias":"测试 AI",
		"agent_enabled":true
	}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload registerRoomResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Room.ID != 10 || payload.AgentSession.ID != 100 {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

func TestHandleMessagesCreatesMessage(t *testing.T) {
	now := time.Now().UTC()
	api := NewServer(fakeCoreStore{
		messageFn: func(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
			if input.RoomID != 10 || input.SourceMessageID != "msg-1" || input.SenderID != "alice" {
				t.Fatalf("unexpected input: %+v", input)
			}
			return core.CreateMessageResult{
				Message: core.Message{
					ID:              20,
					RoomID:          input.RoomID,
					SourceMessageID: input.SourceMessageID,
					SenderID:        input.SenderID,
					Payload:         input.Payload,
					MessageTime:     now,
				},
				Triggered: true,
			}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{
		"room_id":10,
		"source_message_id":"msg-1",
		"sender_id":"alice",
		"message_time":"2026-05-19T10:00:00Z",
		"payload":{"type":"text","text":"虾虾 hello"}
	}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload createMessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Triggered {
		t.Fatal("triggered = false, want true")
	}
}

func TestHandleMemorySearchUsesCapabilityToken(t *testing.T) {
	api := NewServer(fakeCoreStore{
		memoryFn: func(_ context.Context, token string, input core.MemorySearchInput) ([]core.MemoryItem, error) {
			if token != "memory-token" {
				t.Fatalf("token = %q, want memory-token", token)
			}
			if input.RoomID != 0 {
				t.Fatalf("room id should not come from request, got %d", input.RoomID)
			}
			if input.Query != "language" || len(input.Types) != 1 || input.Types[0] != core.MemoryTypePreference {
				t.Fatalf("unexpected input: %+v", input)
			}
			return []core.MemoryItem{{
				ID:      1,
				RoomID:  10,
				Type:    core.MemoryTypePreference,
				Key:     "reply_language",
				Content: "中文回复",
				Status:  core.MemoryStatusActive,
			}}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/internal/memory/search", strings.NewReader(`{
		"room_id":999,
		"query":"language",
		"types":["preference"],
		"limit":5
	}`))
	req.Header.Set("Authorization", "Bearer memory-token")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload memorySearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Key != "reply_language" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}
