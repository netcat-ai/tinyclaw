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
	ingestFn   func(context.Context, core.InboundMessageInput) (core.InboundMessageResult, error)
	completeFn func(context.Context, int64, core.CompleteInvocationInput) (core.InvocationResult, error)
	failFn     func(context.Context, int64, string) (core.InvocationResult, error)
	listFn     func(context.Context, string, int64) ([]core.Delivery, error)
	ackFn      func(context.Context, int64) (core.Delivery, error)
}

func (f fakeCoreStore) IngestCoreMessage(ctx context.Context, input core.InboundMessageInput) (core.InboundMessageResult, error) {
	return f.ingestFn(ctx, input)
}

func (f fakeCoreStore) CompleteCoreInvocation(ctx context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error) {
	return f.completeFn(ctx, invocationID, input)
}

func (f fakeCoreStore) FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (core.InvocationResult, error) {
	return f.failFn(ctx, invocationID, detail)
}

func (f fakeCoreStore) ListCoreDeliveries(ctx context.Context, channel string, afterSeq int64) ([]core.Delivery, error) {
	return f.listFn(ctx, channel, afterSeq)
}

func (f fakeCoreStore) AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error) {
	return f.ackFn(ctx, id)
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

func TestHandleInboundRequiresAPIToken(t *testing.T) {
	api := &Server{
		apiToken: "api-secret",
		core: fakeCoreStore{
			ingestFn: func(context.Context, core.InboundMessageInput) (core.InboundMessageResult, error) {
				t.Fatal("IngestCoreMessage should not be called without auth")
				return core.InboundMessageResult{}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/inbound", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	api.handleInbound(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleInboundReturnsIdempotentMessageResult(t *testing.T) {
	now := time.Now().UTC()
	api := &Server{
		apiToken: "api-secret",
		core: fakeCoreStore{
			ingestFn: func(_ context.Context, input core.InboundMessageInput) (core.InboundMessageResult, error) {
				if input.Channel != "wecom" || input.ChannelRoomID != "room-1" || input.SourceMessageID != "msg-1" {
					t.Fatalf("unexpected input: %+v", input)
				}
				return core.InboundMessageResult{
					Room: core.Room{
						ID:              10,
						TenantID:        core.DefaultTenantID,
						Channel:         input.Channel,
						ChannelRoomID:   input.ChannelRoomID,
						ChannelRoomType: input.ChannelRoomType,
					},
					Message: core.Message{
						ID:              20,
						RoomID:          10,
						SourceMessageID: input.SourceMessageID,
						SenderID:        input.SenderID,
						Payload:         input.Payload,
						DispatchState:   core.DispatchWaiting,
						MessageTime:     now,
					},
					Duplicate: true,
				}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/inbound", strings.NewReader(`{
		"channel":"wecom",
		"channel_room_id":"room-1",
		"channel_room_type":"group",
		"source_message_id":"msg-1",
		"sender_id":"alice",
		"message_time":"2026-05-19T10:00:00Z",
		"payload":{"type":"text","text":"hello"}
	}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.handleInbound(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload inboundResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Duplicate {
		t.Fatal("duplicate = false, want true")
	}
	if payload.Message.DispatchState != core.DispatchWaiting {
		t.Fatalf("dispatch_state = %d, want %d", payload.Message.DispatchState, core.DispatchWaiting)
	}
}

func TestHandleListDeliveriesFiltersByChannelAndSeq(t *testing.T) {
	api := &Server{
		apiToken: "api-secret",
		core: fakeCoreStore{
			listFn: func(_ context.Context, channel string, afterSeq int64) ([]core.Delivery, error) {
				if channel != "wecom" {
					t.Fatalf("channel = %q, want wecom", channel)
				}
				if afterSeq != 12 {
					t.Fatalf("afterSeq = %d, want 12", afterSeq)
				}
				return []core.Delivery{
					{
						ID:           7,
						Seq:          15,
						RoomID:       10,
						InvocationID: 1000,
						Payload:      json.RawMessage(`{"type":"text","text":"hi"}`),
						Status:       core.DeliveryStatusPending,
					},
				}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/deliveries?channel=wecom&seq=12", nil)
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.handleListDeliveries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload deliveriesPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.NextSeq != 15 {
		t.Fatalf("next_seq = %d, want 15", payload.NextSeq)
	}
	if len(payload.Deliveries) != 1 || payload.Deliveries[0].Seq != 15 {
		t.Fatalf("unexpected deliveries: %+v", payload.Deliveries)
	}
}

func TestHandleDeliveryAckRetainsDeliveryRecord(t *testing.T) {
	api := &Server{
		apiToken: "api-secret",
		core: fakeCoreStore{
			ackFn: func(_ context.Context, id int64) (core.Delivery, error) {
				if id != 7 {
					t.Fatalf("id = %d, want 7", id)
				}
				return core.Delivery{
					ID:           7,
					Seq:          15,
					RoomID:       10,
					InvocationID: 1000,
					Payload:      json.RawMessage(`{"type":"text","text":"hi"}`),
					Status:       core.DeliveryStatusAcked,
				}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/deliveries/7/ack", nil)
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.handleDeliveryAction(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload coreDeliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != core.DeliveryStatusAcked {
		t.Fatalf("status = %d, want %d", payload.Status, core.DeliveryStatusAcked)
	}
}

func TestHandleInvocationCompleteCreatesDeliveryResponse(t *testing.T) {
	api := &Server{
		apiToken: "api-secret",
		core: fakeCoreStore{
			completeFn: func(_ context.Context, id int64, input core.CompleteInvocationInput) (core.InvocationResult, error) {
				if id != 1000 {
					t.Fatalf("id = %d, want 1000", id)
				}
				if input.Text != "done" {
					t.Fatalf("text = %q, want done", input.Text)
				}
				return core.InvocationResult{
					Invocation: core.Invocation{ID: 1000, RoomID: 10, Status: core.InvocationStatusCompleted},
					Delivery: &core.Delivery{
						ID:           1,
						Seq:          1,
						RoomID:       10,
						InvocationID: 1000,
						Payload:      json.RawMessage(`{"type":"text","text":"done"}`),
						Status:       core.DeliveryStatusPending,
					},
				}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/invocations/1000/complete", strings.NewReader(`{"text":"done"}`))
	req.Header.Set("Authorization", "Bearer api-secret")
	rec := httptest.NewRecorder()

	api.handleInvocationAction(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
