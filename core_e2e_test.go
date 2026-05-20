package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	httpapi "tinyclaw/internal/api"
	"tinyclaw/internal/core"
	"tinyclaw/internal/executor"
	"tinyclaw/internal/storage"
)

type coreE2ERoomResponse struct {
	Room struct {
		ID int64 `json:"id"`
	} `json:"room"`
	AgentSession struct {
		ID int64 `json:"id"`
	} `json:"agent_session"`
}

type coreE2EMessageResponse struct {
	Message struct {
		ID      int64 `json:"id"`
		Skipped bool  `json:"skipped"`
	} `json:"message"`
	Duplicate bool `json:"duplicate"`
	Triggered bool `json:"triggered"`
}

type coreE2EDeliveryResponse struct {
	ID                   int64           `json:"id"`
	AgentSessionID       int64           `json:"agent_session_id"`
	SourceMessageAfterID int64           `json:"source_message_after_id"`
	SourceMessageUntilID int64           `json:"source_message_until_id"`
	Payload              json.RawMessage `json:"payload"`
	Status               int16           `json:"status"`
}

type coreE2EDeliveriesPageResponse struct {
	Deliveries []coreE2EDeliveryResponse `json:"deliveries"`
	NextID     int64                     `json:"next_id"`
}

func TestCoreModelE2E(t *testing.T) {
	dsn := strings.TrimSpace(envOrDefault("CORE_E2E_DATABASE_URL", os.Getenv("DATABASE_URL")))
	if dsn == "" {
		t.Skip("CORE_E2E_DATABASE_URL or DATABASE_URL is not set")
	}

	ctx := context.Background()
	store, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	coreStore := storage.NewCoreStore(store.DB())
	api := httpapi.NewServer(coreStore, "e2e-token")
	roomName := fmt.Sprintf("e2e-room-%d", time.Now().UnixNano())
	roomResp := postRoomE2E(t, api, roomName)

	contextResp := postMessageE2E(t, api, roomResp.Room.ID, "msg-1", "context message")
	if contextResp.Message.Skipped {
		t.Fatal("context skipped = true, want false")
	}

	duplicateResp := postMessageE2E(t, api, roomResp.Room.ID, "msg-1", "context message")
	if !duplicateResp.Duplicate {
		t.Fatal("duplicate = false, want true")
	}
	if duplicateResp.Message.ID != contextResp.Message.ID {
		t.Fatalf("duplicate message id = %d, want %d", duplicateResp.Message.ID, contextResp.Message.ID)
	}

	triggerResp := postMessageE2E(t, api, roomResp.Room.ID, "msg-2", "@agent handle this")
	if !triggerResp.Triggered {
		t.Fatalf("trigger response did not update agent session: %+v", triggerResp)
	}

	scheduler := executor.NewScheduler(ctx, coreStore, executor.StaticRunner{Text: "done"})
	if !scheduler.RunOnce(ctx) {
		t.Fatal("scheduler RunOnce = false, want true")
	}

	deliveries := listDeliveriesE2E(t, api, "wecom", 0)
	if len(deliveries.Deliveries) != 1 {
		t.Fatalf("deliveries len = %d, want 1", len(deliveries.Deliveries))
	}
	delivery := deliveries.Deliveries[0]
	if delivery.AgentSessionID != roomResp.AgentSession.ID {
		t.Fatalf("delivery agent session id = %d, want %d", delivery.AgentSessionID, roomResp.AgentSession.ID)
	}
	if delivery.SourceMessageAfterID != 0 || delivery.SourceMessageUntilID != triggerResp.Message.ID {
		t.Fatalf("delivery source window = (%d,%d], want (0,%d]", delivery.SourceMessageAfterID, delivery.SourceMessageUntilID, triggerResp.Message.ID)
	}
	var deliveryPayload struct {
		Kind           string `json:"kind"`
		Type           string `json:"type"`
		Text           string `json:"text"`
		App            string `json:"app"`
		ChannelRoomID  string `json:"channel_room_id"`
		RecipientAlias string `json:"recipient_alias"`
	}
	if err := json.Unmarshal(delivery.Payload, &deliveryPayload); err != nil {
		t.Fatalf("decode delivery payload: %v", err)
	}
	if deliveryPayload.Kind != "agent_output" || deliveryPayload.Text != "done" || deliveryPayload.App != "wecom" || deliveryPayload.RecipientAlias != roomName || deliveryPayload.ChannelRoomID != roomName {
		t.Fatalf("delivery payload = %+v, want routed text payload", deliveryPayload)
	}

	acked := ackDeliveryE2E(t, api, delivery.ID)
	if acked.Status != core.DeliveryStatusAcked {
		t.Fatalf("acked status = %d, want %d", acked.Status, core.DeliveryStatusAcked)
	}
}

func postRoomE2E(t *testing.T, api http.Handler, roomName string) coreE2ERoomResponse {
	t.Helper()
	body := fmt.Sprintf(`{
		"channel":"wecom",
		"channel_room_id":%q,
		"channel_room_type":"group",
		"display_name":%q,
		"outbound_alias":%q,
		"agent_enabled":true
	}`, roomName, roomName, roomName)
	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("room status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreE2ERoomResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode room response: %v", err)
	}
	return payload
}

func postMessageE2E(t *testing.T, api http.Handler, roomID int64, sourceID, text string) coreE2EMessageResponse {
	t.Helper()
	body := fmt.Sprintf(`{
		"room_id":%d,
		"source_message_id":%q,
		"sender_id":"alice",
		"message_time":"2026-05-19T10:00:00Z",
		"payload":{"type":"text","text":%q}
	}`, roomID, sourceID, text)
	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreE2EMessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	return payload
}

func listDeliveriesE2E(t *testing.T, api http.Handler, channel string, id int64) coreE2EDeliveriesPageResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/deliveries?channel=%s&id=%d", channel, id), nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list deliveries status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreE2EDeliveriesPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode deliveries response: %v", err)
	}
	return payload
}

func ackDeliveryE2E(t *testing.T, api http.Handler, deliveryID int64) coreE2EDeliveryResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/deliveries/%d/ack", deliveryID), nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack delivery status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreE2EDeliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ack response: %v", err)
	}
	return payload
}
