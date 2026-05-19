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
	"tinyclaw/internal/storage"
)

type coreE2EInboundResponse struct {
	Message    coreE2EMessageResponse     `json:"message"`
	Invocation *coreE2EInvocationResponse `json:"invocation,omitempty"`
	Duplicate  bool                       `json:"duplicate"`
	Triggered  bool                       `json:"triggered"`
	Appended   bool                       `json:"appended"`
}

type coreE2EMessageResponse struct {
	ID      int64 `json:"id"`
	Skipped bool  `json:"skipped"`
}

type coreE2EInvocationResponse struct {
	ID int64 `json:"id"`
}

type coreE2EDeliveryResponse struct {
	ID           int64 `json:"id"`
	InvocationID int64 `json:"invocation_id"`
	Status       int16 `json:"status"`
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
	roomID := fmt.Sprintf("e2e-room-%d", time.Now().UnixNano())

	contextResp := postInboundE2E(t, api, roomID, "msg-1", "context message")
	if contextResp.Message.Skipped {
		t.Fatal("context skipped = true, want false")
	}

	duplicateResp := postInboundE2E(t, api, roomID, "msg-1", "context message")
	if !duplicateResp.Duplicate {
		t.Fatal("duplicate = false, want true")
	}
	if duplicateResp.Message.ID != contextResp.Message.ID {
		t.Fatalf("duplicate message id = %d, want %d", duplicateResp.Message.ID, contextResp.Message.ID)
	}

	triggerResp := postInboundE2E(t, api, roomID, "msg-2", "@agent handle this")
	if !triggerResp.Triggered || triggerResp.Invocation == nil {
		t.Fatalf("trigger response did not create invocation: %+v", triggerResp)
	}
	invocationID := triggerResp.Invocation.ID
	if invocationID < 1000 {
		t.Fatalf("invocation id = %d, want >= 1000", invocationID)
	}

	appendResp := postInboundE2E(t, api, roomID, "msg-3", "more context")
	if !appendResp.Appended {
		t.Fatal("appended = false, want true")
	}

	started, err := coreStore.StartCoreInvocation(ctx, invocationID)
	if err != nil {
		t.Fatalf("start invocation: %v", err)
	}
	if started.StartMessageID != appendResp.Message.ID || started.LastSeenMessageID != appendResp.Message.ID {
		t.Fatalf("started cursor = (%d,%d), want %d", started.StartMessageID, started.LastSeenMessageID, appendResp.Message.ID)
	}
	contextMessages, err := coreStore.ListCoreInvocationContextMessages(ctx, invocationID)
	if err != nil {
		t.Fatalf("list invocation context messages: %v", err)
	}
	if len(contextMessages) != 3 {
		t.Fatalf("context messages len = %d, want 3", len(contextMessages))
	}

	liveResp := postInboundE2E(t, api, roomID, "msg-4", "live follow-up")
	if !liveResp.Appended {
		t.Fatal("live appended = false, want true")
	}
	newMessages, err := coreStore.ReadCoreInvocationNewMessages(ctx, invocationID)
	if err != nil {
		t.Fatalf("read new invocation messages: %v", err)
	}
	if len(newMessages) != 1 || newMessages[0].ID != liveResp.Message.ID {
		t.Fatalf("new messages = %+v, want message id %d", newMessages, liveResp.Message.ID)
	}
	newMessages, err = coreStore.ReadCoreInvocationNewMessages(ctx, invocationID)
	if err != nil {
		t.Fatalf("read new invocation messages again: %v", err)
	}
	if len(newMessages) != 0 {
		t.Fatalf("new messages after cursor advance = %d, want 0", len(newMessages))
	}

	completeResp := postInvocationActionE2E(t, api, invocationID, "complete", `{"text":"done"}`)
	delivery, ok := completeResp["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("completion response has no delivery: %+v", completeResp)
	}
	if delivery["status"] != float64(core.DeliveryStatusPending) {
		t.Fatalf("delivery status = %v, want %d", delivery["status"], core.DeliveryStatusPending)
	}

	deliveries := listDeliveriesE2E(t, api, "wecom", 0)
	if len(deliveries.Deliveries) != 1 {
		t.Fatalf("deliveries len = %d, want 1", len(deliveries.Deliveries))
	}
	if deliveries.Deliveries[0].InvocationID != invocationID {
		t.Fatalf("delivery invocation id = %d, want %d", deliveries.Deliveries[0].InvocationID, invocationID)
	}

	acked := ackDeliveryE2E(t, api, deliveries.Deliveries[0].ID)
	if acked.Status != core.DeliveryStatusAcked {
		t.Fatalf("acked status = %d, want %d", acked.Status, core.DeliveryStatusAcked)
	}
}

func postInboundE2E(t *testing.T, api http.Handler, roomID, sourceID, text string) coreE2EInboundResponse {
	t.Helper()
	body := fmt.Sprintf(`{
		"channel":"wecom",
		"channel_room_id":%q,
		"channel_room_type":"group",
		"source_message_id":%q,
		"sender_id":"alice",
		"message_time":"2026-05-19T10:00:00Z",
		"payload":{"type":"text","text":%q}
	}`, roomID, sourceID, text)
	req := httptest.NewRequest(http.MethodPost, "/api/inbound", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreE2EInboundResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode inbound response: %v", err)
	}
	return payload
}

func postInvocationActionE2E(t *testing.T, api http.Handler, invocationID int64, action string, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/invocations/%d/%s", invocationID, action), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invocation action status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode invocation response: %v", err)
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
