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
)

func TestCoreModelE2E(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CORE_E2E_DATABASE_URL"))
	if dsn == "" {
		t.Skip("CORE_E2E_DATABASE_URL is not set")
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

	api := &controlAPI{
		core:     store,
		apiToken: "e2e-token",
	}
	roomID := fmt.Sprintf("e2e-room-%d", time.Now().UnixNano())

	contextResp := postInboundE2E(t, api, roomID, "msg-1", "context message")
	if contextResp.Message.DispatchState != dispatchWaiting {
		t.Fatalf("context dispatch_state = %d, want %d", contextResp.Message.DispatchState, dispatchWaiting)
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
	if triggerResp.Message.DispatchState != invocationID {
		t.Fatalf("trigger dispatch_state = %d, want %d", triggerResp.Message.DispatchState, invocationID)
	}

	appendResp := postInboundE2E(t, api, roomID, "msg-3", "more context")
	if !appendResp.Appended {
		t.Fatal("appended = false, want true")
	}
	if appendResp.Message.DispatchState != invocationID {
		t.Fatalf("append dispatch_state = %d, want %d", appendResp.Message.DispatchState, invocationID)
	}

	completeResp := postInvocationActionE2E(t, api, invocationID, "complete", `{"text":"done"}`)
	delivery, ok := completeResp["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("completion response has no delivery: %+v", completeResp)
	}
	if delivery["status"] != deliveryStatusPending {
		t.Fatalf("delivery status = %v, want %s", delivery["status"], deliveryStatusPending)
	}

	deliveries := listDeliveriesE2E(t, api, "wecom", 0)
	if len(deliveries.Deliveries) != 1 {
		t.Fatalf("deliveries len = %d, want 1", len(deliveries.Deliveries))
	}
	if deliveries.Deliveries[0].InvocationID != invocationID {
		t.Fatalf("delivery invocation id = %d, want %d", deliveries.Deliveries[0].InvocationID, invocationID)
	}

	acked := ackDeliveryE2E(t, api, deliveries.Deliveries[0].ID)
	if acked.Status != deliveryStatusAcked {
		t.Fatalf("acked status = %s, want %s", acked.Status, deliveryStatusAcked)
	}
}

func postInboundE2E(t *testing.T, api *controlAPI, roomID, sourceID, text string) inboundResponse {
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
	api.handleInbound(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload inboundResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode inbound response: %v", err)
	}
	return payload
}

func postInvocationActionE2E(t *testing.T, api *controlAPI, invocationID int64, action string, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/invocations/%d/%s", invocationID, action), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.handleInvocationAction(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invocation action status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode invocation response: %v", err)
	}
	return payload
}

func listDeliveriesE2E(t *testing.T, api *controlAPI, channel string, seq int64) deliveriesPageResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/deliveries?channel=%s&seq=%d", channel, seq), nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.handleListDeliveries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list deliveries status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload deliveriesPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode deliveries response: %v", err)
	}
	return payload
}

func ackDeliveryE2E(t *testing.T, api *controlAPI, deliveryID int64) coreDeliveryResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/deliveries/%d/ack", deliveryID), nil)
	req.Header.Set("Authorization", "Bearer e2e-token")
	rec := httptest.NewRecorder()
	api.handleDeliveryAction(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack delivery status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload coreDeliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ack response: %v", err)
	}
	return payload
}
