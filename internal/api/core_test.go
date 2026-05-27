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
	registerFn    func(context.Context, core.RegisterRoomInput) (core.RegisterRoomResult, error)
	messageFn     func(context.Context, core.CreateMessageInput) (core.CreateMessageResult, error)
	listFn        func(context.Context, string, int64) ([]core.Delivery, error)
	ackFn         func(context.Context, int64) (core.Delivery, error)
	memoryFn      func(context.Context, string, core.MemorySearchInput) ([]core.MemoryItem, error)
	authFn        func(context.Context, string, string) (core.APIClient, error)
	roomsFn       func(context.Context, int) ([]core.AdminRoomSummary, error)
	timelineFn    func(context.Context, int64, int64, int) (core.AdminRoomTimeline, error)
	adminMemFn    func(context.Context, core.AdminMemoryListInput) ([]core.MemoryItem, error)
	agentsFn      func(context.Context) ([]core.Agent, error)
	getAgentFn    func(context.Context, int64) (core.Agent, error)
	createAgentFn func(context.Context, core.UpsertAgentInput) (core.Agent, error)
	updateAgentFn func(context.Context, int64, core.UpsertAgentInput) (core.Agent, error)
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

func (f fakeCoreStore) AuthenticateAPIClient(ctx context.Context, clientID string, secret string) (core.APIClient, error) {
	return f.authFn(ctx, clientID, secret)
}

func (f fakeCoreStore) ListAdminRooms(ctx context.Context, limit int) ([]core.AdminRoomSummary, error) {
	return f.roomsFn(ctx, limit)
}

func (f fakeCoreStore) GetAdminRoomTimeline(ctx context.Context, roomID int64, beforeMessageID int64, limit int) (core.AdminRoomTimeline, error) {
	return f.timelineFn(ctx, roomID, beforeMessageID, limit)
}

func (f fakeCoreStore) ListAdminRoomMemory(ctx context.Context, input core.AdminMemoryListInput) ([]core.MemoryItem, error) {
	return f.adminMemFn(ctx, input)
}

func (f fakeCoreStore) ListAgents(ctx context.Context) ([]core.Agent, error) {
	return f.agentsFn(ctx)
}

func (f fakeCoreStore) GetAgent(ctx context.Context, id int64) (core.Agent, error) {
	return f.getAgentFn(ctx, id)
}

func (f fakeCoreStore) CreateAgent(ctx context.Context, input core.UpsertAgentInput) (core.Agent, error) {
	return f.createAgentFn(ctx, input)
}

func (f fakeCoreStore) UpdateAgent(ctx context.Context, id int64, input core.UpsertAgentInput) (core.Agent, error) {
	return f.updateAgentFn(ctx, id, input)
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

func TestHandleListDeliveriesAcceptsMultipleChannels(t *testing.T) {
	var calls []string
	api := NewServer(fakeCoreStore{
		listFn: func(_ context.Context, channel string, afterID int64) ([]core.Delivery, error) {
			calls = append(calls, channel)
			if afterID != 10 {
				t.Fatalf("afterID = %d, want 10", afterID)
			}
			switch channel {
			case "wecom":
				return []core.Delivery{{ID: 12, Payload: json.RawMessage(`{"channel":"wecom"}`)}}, nil
			case "wechat":
				return []core.Delivery{{ID: 11, Payload: json.RawMessage(`{"channel":"wechat"}`)}}, nil
			default:
				t.Fatalf("unexpected channel %q", channel)
			}
			return nil, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/deliveries?channels=wecom,wechat&after_id=10", nil)
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Join(calls, ",") != "wecom,wechat" {
		t.Fatalf("channels = %v, want wecom,wechat", calls)
	}
	var payload deliveriesPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Deliveries) != 2 || payload.Deliveries[0].ID != 11 || payload.Deliveries[1].ID != 12 {
		t.Fatalf("deliveries = %+v, want sorted ids 11,12", payload.Deliveries)
	}
}

func TestHandleListDeliveriesAcceptsLegacyIDCursor(t *testing.T) {
	api := NewServer(fakeCoreStore{
		listFn: func(_ context.Context, channel string, afterID int64) ([]core.Delivery, error) {
			if channel != "wecom" || afterID != 44 {
				t.Fatalf("channel/afterID = %s/%d, want wecom/44", channel, afterID)
			}
			return nil, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/deliveries?channels=wecom&id=44", nil)
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleDeliveryAckUsesDeliveryID(t *testing.T) {
	api := NewServer(fakeCoreStore{
		ackFn: func(_ context.Context, id int64) (core.Delivery, error) {
			if id != 42 {
				t.Fatalf("ack id = %d, want 42", id)
			}
			return core.Delivery{ID: id, Status: core.DeliveryStatusAcked}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/deliveries/42/ack", nil)
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreDeliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != 42 || payload.Status != core.DeliveryStatusAcked {
		t.Fatalf("payload = %+v, want acked delivery 42", payload)
	}
}

func TestHandleMessagesSuppressesAgentTriggerForDrawCommand(t *testing.T) {
	commands := &fakeCommandHandler{}
	api := NewServerWithCommandHandler(fakeCoreStore{
		messageFn: func(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
			if !input.SuppressAgentTrigger {
				t.Fatal("SuppressAgentTrigger = false, want true")
			}
			return core.CreateMessageResult{
				Message: core.Message{
					ID:              20,
					RoomID:          input.RoomID,
					SourceMessageID: input.SourceMessageID,
					Source:          input.Source,
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
					ID:      100,
					RoomID:  10,
					Enabled: input.AgentEnabled,
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

func TestHandleAdminRoomsRequiresAdminClient(t *testing.T) {
	api := NewServer(fakeCoreStore{
		authFn: func(_ context.Context, clientID string, secret string) (core.APIClient, error) {
			if clientID != "admin" || secret != "secret" {
				t.Fatalf("credentials = %q/%q, want admin/secret", clientID, secret)
			}
			return core.APIClient{Permissions: []string{core.APIClientPermissionAdmin}}, nil
		},
		roomsFn: func(_ context.Context, limit int) ([]core.AdminRoomSummary, error) {
			if limit != 20 {
				t.Fatalf("limit = %d, want 20", limit)
			}
			return []core.AdminRoomSummary{{
				Room: core.Room{
					ID:              10,
					TenantID:        core.DefaultTenantID,
					Channel:         "wecom",
					ChannelRoomID:   "room-1",
					ChannelRoomType: core.RoomChatTypeGroup,
					OutboundAlias:   "测试群",
				},
				AgentSession: core.AgentSession{
					ID:      100,
					RoomID:  10,
					Enabled: true,
				},
				PendingDeliveryCount: 2,
			}}, nil
		},
	}, "api-secret")

	req := httptest.NewRequest(http.MethodGet, "/admin/api/rooms?limit=20", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload adminRoomsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Rooms) != 1 || payload.Rooms[0].PendingDeliveryCount != 2 {
		t.Fatalf("unexpected rooms response: %+v", payload.Rooms)
	}
}

func TestHandleAdminRoomsAcceptsBuiltInAdminSecret(t *testing.T) {
	api := NewServerWithCommandHandler(fakeCoreStore{
		authFn: func(context.Context, string, string) (core.APIClient, error) {
			t.Fatal("AuthenticateAPIClient should not be called for built-in admin")
			return core.APIClient{}, nil
		},
		roomsFn: func(_ context.Context, limit int) ([]core.AdminRoomSummary, error) {
			if limit != 10 {
				t.Fatalf("limit = %d, want 10", limit)
			}
			return nil, nil
		},
	}, nil, "api-secret", "admin-secret")

	req := httptest.NewRequest(http.MethodGet, "/admin/api/rooms?limit=10", nil)
	req.SetBasicAuth("admin", "admin-secret")
	rec := httptest.NewRecorder()

	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleAdminAgentsCreatesMutableAgent(t *testing.T) {
	api := NewServerWithCommandHandler(fakeCoreStore{
		createAgentFn: func(_ context.Context, input core.UpsertAgentInput) (core.Agent, error) {
			if input.Key != "product" || input.DisplayName != "产品" || input.Prompt != "You are product." {
				t.Fatalf("unexpected input: %+v", input)
			}
			if string(input.AllowedTools) != `["memory_search"]` {
				t.Fatalf("allowed_tools = %s, want memory_search", input.AllowedTools)
			}
			return core.Agent{
				ID:           7,
				Key:          input.Key,
				DisplayName:  input.DisplayName,
				Prompt:       input.Prompt,
				AllowedTools: input.AllowedTools,
				Enabled:      input.Enabled,
			}, nil
		},
	}, nil, "api-secret", "admin-secret")

	req := httptest.NewRequest(http.MethodPost, "/admin/api/agents", strings.NewReader(`{
		"key":"product",
		"display_name":"产品",
		"prompt":"You are product.",
		"allowed_tools":["memory_search"],
		"enabled":true
	}`))
	req.SetBasicAuth("admin", "admin-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload adminAgentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Agent.ID != 7 || payload.Agent.Key != "product" || !payload.Agent.Enabled {
		t.Fatalf("agent = %+v, want product", payload.Agent)
	}
}

func TestHandleAdminAgentsUpdatesAgentWithoutVersioning(t *testing.T) {
	api := NewServerWithCommandHandler(fakeCoreStore{
		updateAgentFn: func(_ context.Context, id int64, input core.UpsertAgentInput) (core.Agent, error) {
			if id != 7 {
				t.Fatalf("id = %d, want 7", id)
			}
			if input.Key != "product" || input.Prompt != "Updated prompt." || input.Enabled {
				t.Fatalf("unexpected input: %+v", input)
			}
			return core.Agent{
				ID:           id,
				Key:          input.Key,
				DisplayName:  input.DisplayName,
				Prompt:       input.Prompt,
				AllowedTools: input.AllowedTools,
				Enabled:      input.Enabled,
			}, nil
		},
	}, nil, "api-secret", "admin-secret")

	req := httptest.NewRequest(http.MethodPut, "/admin/api/agents/7", strings.NewReader(`{
		"key":"product",
		"display_name":"产品",
		"prompt":"Updated prompt.",
		"allowed_tools":[],
		"enabled":false
	}`))
	req.SetBasicAuth("admin", "admin-secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload adminAgentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Agent.Enabled {
		t.Fatalf("enabled = true, want false")
	}
}

func TestHandleMessagesCreatesMessage(t *testing.T) {
	now := time.Now().UTC()
	api := NewServer(fakeCoreStore{
		messageFn: func(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
			if input.RoomID != 10 || input.SourceMessageID != "msg-1" || input.Source != "wecom" || input.SenderID != "alice" {
				t.Fatalf("unexpected input: %+v", input)
			}
			return core.CreateMessageResult{
				Message: core.Message{
					ID:              20,
					RoomID:          input.RoomID,
					SourceMessageID: input.SourceMessageID,
					Source:          input.Source,
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
		"source":"wecom",
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
	if payload.Message.Source != "wecom" {
		t.Fatalf("message source = %+v, want wecom", payload.Message)
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
