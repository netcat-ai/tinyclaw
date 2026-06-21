package main

import (
	"bytes"
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
		ID int64 `json:"id"`
	} `json:"message"`
	Duplicate bool `json:"duplicate"`
	Triggered bool `json:"triggered"`
}

type coreE2EDeliveryResponse struct {
	ID                  int64           `json:"id"`
	AgentSessionID      int64           `json:"agent_session_id"`
	SourceMessageFromID int64           `json:"source_message_from_id"`
	SourceMessageToID   int64           `json:"source_message_to_id"`
	Payload             json.RawMessage `json:"payload"`
	Status              int16           `json:"status"`
}

func TestCoreE2EWechatTextAndImageDeliveries(t *testing.T) {
	dsn := os.Getenv("CORE_E2E_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("CORE_E2E_DATABASE_URL or DATABASE_URL is not set")
	}

	ctx := context.Background()
	store, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	resetCoreE2EDatabase(t, store)

	coreStore := storage.NewCoreStore(store.DB())
	api := httpapi.NewServer(coreStore, "e2e-token")

	suffix := time.Now().UnixNano()
	channel := fmt.Sprintf("wechat-e2e-%d", suffix)
	channelRoomID := fmt.Sprintf("wechat-e2e-%d@chatroom", suffix)
	roomResp := postRoomForChannelE2E(t, api, channel, channelRoomID, "测试群", `{"mode":"always"}`)
	textResp := postMessageE2E(t, api, roomResp.Room.ID, "wechat-msg-1", "虾虾，你好呀")
	if !textResp.Triggered {
		t.Fatal("wechat text triggered = false, want true")
	}

	scheduler := executor.NewScheduler(ctx, coreStore, executor.StaticRunner{Text: "收到：你好呀"})
	if !scheduler.RunOnce(ctx) {
		t.Fatal("scheduler RunOnce = false, want true")
	}
	deliveries := listDeliveriesE2E(t, api, channel)
	if len(deliveries.Deliveries) != 1 {
		t.Fatalf("wechat deliveries len = %d, want 1", len(deliveries.Deliveries))
	}
	var textPayload map[string]any
	if err := json.Unmarshal(deliveries.Deliveries[0].Payload, &textPayload); err != nil {
		t.Fatalf("decode text delivery: %v", err)
	}
	if textPayload["app"] != channel || textPayload["channel"] != channel || textPayload["recipient_alias"] != "测试群" || textPayload["channel_room_id"] != channelRoomID {
		t.Fatalf("text delivery route = %+v", textPayload)
	}
	if textPayload["type"] != "text" || textPayload["text"] != "收到：你好呀" {
		t.Fatalf("text delivery payload = %+v", textPayload)
	}
	ackDeliveryE2E(t, api, deliveries.Deliveries[0].ID)
}

type coreE2EDeliveriesPageResponse struct {
	Deliveries []coreE2EDeliveryResponse `json:"deliveries"`
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
	defer func() { _ = store.Close() }()
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	resetCoreE2EDatabase(t, store)

	coreStore := storage.NewCoreStore(store.DB())
	api := httpapi.NewServer(coreStore, "e2e-token")
	apiServer := httptest.NewServer(api)
	defer apiServer.Close()
	suffix := time.Now().UnixNano()
	channel := fmt.Sprintf("core-e2e-%d", suffix)
	roomName := fmt.Sprintf("e2e-room-%d", suffix)
	roomResp := postRoomForChannelE2E(t, api, channel, roomName, roomName, "")

	contextResp := postMessageE2E(t, api, roomResp.Room.ID, "msg-1", "context message")

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

	scheduler := executor.NewScheduler(ctx, coreStore, executor.StaticRunner{
		Text: "done",
		MemoryWriteProposals: []core.MemoryWriteProposal{{
			Op:      core.MemoryWriteOpSetPreference,
			Key:     "reply_language",
			Content: "Use Chinese by default.",
		}},
	})
	scheduler.SetMemorySearchURL(apiServer.URL + "/internal/memory/search")
	if !scheduler.RunOnce(ctx) {
		t.Fatal("scheduler RunOnce = false, want true")
	}
	memoryWorker := executor.NewMemoryWriteWorker(ctx, coreStore)
	if !memoryWorker.RunOnce(ctx) {
		t.Fatal("memory worker RunOnce = false, want true")
	}

	token, err := coreStore.CreateMemoryCapabilityToken(ctx, core.AgentRun{
		AgentSessionID:      roomResp.AgentSession.ID,
		RoomID:              roomResp.Room.ID,
		SourceMessageFromID: contextResp.Message.ID,
		SourceMessageToID:   triggerResp.Message.ID,
	}, time.Minute)
	if err != nil {
		t.Fatalf("create memory token: %v", err)
	}
	memoryItems := searchMemoryE2E(t, api, token, "Chinese", true)
	if len(memoryItems) != 1 {
		t.Fatalf("memory item count = %d, want 1", len(memoryItems))
	}
	if memoryItems[0].RoomID != roomResp.Room.ID || memoryItems[0].Type != core.MemoryTypePreference || memoryItems[0].Key != "reply_language" {
		t.Fatalf("memory item = %+v, want room preference", memoryItems[0])
	}
	if memoryItems[0].SourceMessageToID != triggerResp.Message.ID {
		t.Fatalf("memory source to = %d, want %d", memoryItems[0].SourceMessageToID, triggerResp.Message.ID)
	}

	secondTriggerResp := postMessageE2E(t, api, roomResp.Room.ID, "msg-3", "@agent use memory")
	if !secondTriggerResp.Triggered {
		t.Fatalf("second trigger response did not update agent session: %+v", secondTriggerResp)
	}
	memoryRunner := &memorySearchingRunner{t: t}
	memoryScheduler := executor.NewScheduler(ctx, coreStore, memoryRunner)
	memoryScheduler.SetMemorySearchURL(apiServer.URL + "/internal/memory/search")
	if !memoryScheduler.RunOnce(ctx) {
		t.Fatal("memory scheduler RunOnce = false, want true")
	}
	if memoryRunner.seenContent != "Use Chinese by default." {
		t.Fatalf("runner memory content = %q, want stored preference", memoryRunner.seenContent)
	}

	deliveries := listDeliveriesE2E(t, api, channel)
	if len(deliveries.Deliveries) != 2 {
		t.Fatalf("deliveries len = %d, want 2", len(deliveries.Deliveries))
	}
	delivery := deliveries.Deliveries[0]
	if delivery.AgentSessionID != roomResp.AgentSession.ID {
		t.Fatalf("delivery agent session id = %d, want %d", delivery.AgentSessionID, roomResp.AgentSession.ID)
	}
	if delivery.SourceMessageFromID != contextResp.Message.ID || delivery.SourceMessageToID != triggerResp.Message.ID {
		t.Fatalf("delivery source window = [%d,%d], want [%d,%d]", delivery.SourceMessageFromID, delivery.SourceMessageToID, contextResp.Message.ID, triggerResp.Message.ID)
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
	if deliveryPayload.Kind != "agent_output" || deliveryPayload.Text != "done" || deliveryPayload.App != channel || deliveryPayload.RecipientAlias != roomName || deliveryPayload.ChannelRoomID != roomName {
		t.Fatalf("delivery payload = %+v, want routed text payload", deliveryPayload)
	}

	acked := ackDeliveryE2E(t, api, delivery.ID)
	if acked.Status != core.DeliveryStatusAcked {
		t.Fatalf("acked status = %d, want %d", acked.Status, core.DeliveryStatusAcked)
	}
	ackedAgain := ackDeliveryE2E(t, api, delivery.ID)
	if ackedAgain.Status != core.DeliveryStatusAcked {
		t.Fatalf("second ack status = %d, want %d", ackedAgain.Status, core.DeliveryStatusAcked)
	}
}

type memorySearchingRunner struct {
	t           *testing.T
	seenContent string
}

func (r *memorySearchingRunner) RunAgent(ctx context.Context, run executor.AgentRunRequest) (core.AgentRunResult, error) {
	r.t.Helper()
	if run.MemorySearchURL == "" {
		r.t.Fatal("memory search URL is empty")
	}
	if run.MemorySearchToken == "" {
		r.t.Fatal("memory search token is empty")
	}
	body := []byte(`{"query":"Chinese","types":["preference"],"limit":5}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, run.MemorySearchURL, bytes.NewReader(body))
	if err != nil {
		r.t.Fatalf("create memory search request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+run.MemorySearchToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("call memory search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		Items []core.MemoryItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		r.t.Fatalf("decode memory search: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("memory search status = %d payload=%+v", resp.StatusCode, payload)
	}
	if len(payload.Items) != 1 {
		r.t.Fatalf("memory search returned %d items, want 1", len(payload.Items))
	}
	r.seenContent = payload.Items[0].Content
	return core.AgentRunResult{FinalOutput: "remembered"}, nil
}

func searchMemoryE2E(t *testing.T, api http.Handler, token string, query string, includeInactive bool) []core.MemoryItem {
	t.Helper()
	body := fmt.Sprintf(`{
		"room_id":999,
		"query":%q,
		"types":["preference"],
		"include_inactive":%t
	}`, query, includeInactive)
	req := httptest.NewRequest(http.MethodPost, "/internal/memory/search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("memory search status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Items []core.MemoryItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode memory search response: %v", err)
	}
	return payload.Items
}

func postRoomForChannelE2E(t *testing.T, api http.Handler, channel, channelRoomID, outboundAlias, triggerPolicy string) coreE2ERoomResponse {
	t.Helper()
	triggerPolicyField := ""
	if strings.TrimSpace(triggerPolicy) != "" {
		triggerPolicyField = fmt.Sprintf(`,
		"trigger_policy":%s`, triggerPolicy)
	}
	body := fmt.Sprintf(`{
		"channel":%q,
		"channel_room_id":%q,
		"channel_room_type":"group",
		"display_name":%q,
		"outbound_alias":%q,
		"agent_enabled":true%s
	}`, channel, channelRoomID, outboundAlias, outboundAlias, triggerPolicyField)
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

func listDeliveriesE2E(t *testing.T, api http.Handler, channel string) coreE2EDeliveriesPageResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/deliveries?channels=%s", channel), nil)
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

func resetCoreE2EDatabase(t *testing.T, store *Store) {
	t.Helper()
	_, err := store.DB().Exec(`
		TRUNCATE
			memory_change_audit,
			memory_write_jobs,
			memory_capability_tokens,
			memory_items,
			deliveries,
			agents,
			agent_sessions,
			messages,
			rooms,
			api_clients
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		t.Fatalf("reset e2e database: %v", err)
	}
}
