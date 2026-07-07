package msghub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestIlinkActorRoundTrip(t *testing.T) {
	store := NewMemoryStore()
	store.UpsertActor(Actor{
		ActorKey:          "terminal-wechat",
		Token:             "terminal-token",
		Kind:              ActorKindTerminal,
		Name:              "Linux WeChat",
		Channel:           "wechat",
		ProviderAccountID: "wx-account-1",
	})
	store.UpsertActor(Actor{
		ActorKey:    "aiagent-clawman",
		Token:       "ai-token",
		Kind:        ActorKindAIAgent,
		Name:        "Clawman AI",
		Permissions: []string{PermissionSubscribeAll},
	})
	runIlinkActorRoundTrip(t, NewServer(store))
}

func TestPostgresIlinkActorRoundTrip(t *testing.T) {
	dsn := os.Getenv("MSGHUB_E2E_DATABASE_URL")
	if dsn == "" {
		t.Skip("MSGHUB_E2E_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := OpenPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close postgres store: %v", err)
		}
	})
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
		TRUNCATE
			capability_tokens,
			actor_inbox,
			actor_tasks,
			actor_subscriptions,
			messages,
			rooms,
			actors
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("reset msghub schema: %v", err)
	}
	if _, err := store.SeedActor(ctx, Actor{
		ActorKey:          "terminal-wechat",
		Token:             "terminal-token",
		Kind:              ActorKindTerminal,
		Name:              "Linux WeChat",
		Channel:           "wechat",
		ProviderAccountID: "wx-account-1",
	}); err != nil {
		t.Fatalf("seed terminal actor: %v", err)
	}
	if _, err := store.SeedActor(ctx, Actor{
		ActorKey:    "aiagent-clawman",
		Token:       "ai-token",
		Kind:        ActorKindAIAgent,
		Name:        "Clawman AI",
		Permissions: []string{PermissionSubscribeAll},
	}); err != nil {
		t.Fatalf("seed ai actor: %v", err)
	}
	runIlinkActorRoundTrip(t, NewServer(store))
}

func runIlinkActorRoundTrip(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()

	var terminalSend SendMessageResponse
	postJSON(t, server.URL+"/ilink/sendmessage", "terminal-token", map[string]any{
		"room": map[string]any{
			"channel":             "wechat",
			"provider_account_id": "wx-account-1",
			"external_room_id":    "alice",
			"room_type":           "direct",
			"display_name":        "Alice",
			"outbound_target":     "alice",
		},
		"external_message_id": "wechat-message-1",
		"sender_id":           "alice",
		"sender_name":         "Alice",
		"message_time":        time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		"message_type":        "text",
		"body": map[string]any{
			"text": "hello",
		},
	}, &terminalSend)
	if !terminalSend.Accepted || terminalSend.Message == nil {
		t.Fatalf("terminal send response = %+v, want accepted message", terminalSend)
	}

	var aiUpdates GetUpdatesResponse
	postJSON(t, server.URL+"/ilink/getupdates", "ai-token", map[string]any{"limit": 10}, &aiUpdates)
	if len(aiUpdates.Updates) != 1 {
		t.Fatalf("ai updates count = %d, want 1", len(aiUpdates.Updates))
	}
	if aiUpdates.Updates[0].EventType != EventMessageReceived {
		t.Fatalf("ai update event = %q, want %q", aiUpdates.Updates[0].EventType, EventMessageReceived)
	}
	var receivedPayload struct {
		ContextToken string      `json:"context_token"`
		Message      MessageView `json:"message"`
		Room         RoomView    `json:"room"`
	}
	if err := json.Unmarshal(aiUpdates.Updates[0].Payload, &receivedPayload); err != nil {
		t.Fatalf("decode ai update payload: %v", err)
	}
	if receivedPayload.ContextToken == "" {
		t.Fatalf("context_token is empty")
	}
	if receivedPayload.Message.ExternalMessageID != "wechat-message-1" {
		t.Fatalf("message id = %q, want wechat-message-1", receivedPayload.Message.ExternalMessageID)
	}

	var aiSend SendMessageResponse
	postJSON(t, server.URL+"/ilink/sendmessage", "ai-token", map[string]any{
		"context_token": receivedPayload.ContextToken,
		"message_type":  "text",
		"body": map[string]any{
			"text": "pong",
		},
	}, &aiSend)
	if !aiSend.Accepted || aiSend.Task == nil {
		t.Fatalf("ai send response = %+v, want accepted task", aiSend)
	}
	if aiSend.Task.TaskType != TaskTypeSendMessage {
		t.Fatalf("task type = %q, want %q", aiSend.Task.TaskType, TaskTypeSendMessage)
	}

	var terminalUpdates GetUpdatesResponse
	postJSON(t, server.URL+"/ilink/getupdates", "terminal-token", map[string]any{"limit": 10}, &terminalUpdates)
	if len(terminalUpdates.Updates) != 1 {
		t.Fatalf("terminal updates count = %d, want 1", len(terminalUpdates.Updates))
	}
	if terminalUpdates.Updates[0].EventType != EventMessageSendRequested {
		t.Fatalf("terminal update event = %q, want %q", terminalUpdates.Updates[0].EventType, EventMessageSendRequested)
	}
	var taskPayload struct {
		Task ActorTaskView `json:"task"`
		Room RoomView      `json:"room"`
	}
	if err := json.Unmarshal(terminalUpdates.Updates[0].Payload, &taskPayload); err != nil {
		t.Fatalf("decode terminal update payload: %v", err)
	}
	if taskPayload.Task.ID != aiSend.Task.ID {
		t.Fatalf("terminal task id = %d, want %d", taskPayload.Task.ID, aiSend.Task.ID)
	}
	var sendPayload struct {
		MessageType      string `json:"message_type"`
		ReplyToMessageID int64  `json:"reply_to_message_id"`
		Body             struct {
			Text string `json:"text"`
		} `json:"body"`
	}
	if err := json.Unmarshal(taskPayload.Task.Payload, &sendPayload); err != nil {
		t.Fatalf("decode send task payload: %v", err)
	}
	if sendPayload.Body.Text != "pong" {
		t.Fatalf("send task text = %q, want pong", sendPayload.Body.Text)
	}
	if sendPayload.ReplyToMessageID != terminalSend.Message.ID {
		t.Fatalf("reply_to_message_id = %d, want %d", sendPayload.ReplyToMessageID, terminalSend.Message.ID)
	}

	var terminalAck AckResponse
	postJSON(t, server.URL+"/ilink/ack", "terminal-token", map[string]any{
		"update_ids": []int64{terminalUpdates.Updates[0].ID},
		"task_results": []map[string]any{
			{
				"task_id": taskPayload.Task.ID,
				"status":  TaskStatusAcked,
			},
		},
	}, &terminalAck)
	if len(terminalAck.Tasks) != 1 || terminalAck.Tasks[0].Status != TaskStatusAcked {
		t.Fatalf("terminal ack = %+v, want acked task", terminalAck)
	}

	var aiStatusUpdates GetUpdatesResponse
	postJSON(t, server.URL+"/ilink/getupdates", "ai-token", map[string]any{
		"after_id": aiUpdates.Updates[0].ID,
		"limit":    10,
	}, &aiStatusUpdates)
	if len(aiStatusUpdates.Updates) != 1 {
		t.Fatalf("ai status updates count = %d, want 1", len(aiStatusUpdates.Updates))
	}
	if aiStatusUpdates.Updates[0].EventType != EventMessageSent {
		t.Fatalf("ai status event = %q, want %q", aiStatusUpdates.Updates[0].EventType, EventMessageSent)
	}
}

func TestOnlyIlinkRoutesAreExposed(t *testing.T) {
	server := httptest.NewServer(NewServer(NewMemoryStore()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/messages", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("post non-ilink route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-ilink route status = %d, want 404", resp.StatusCode)
	}
}

func TestAckDoesNotRewriteTerminalTaskStatus(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	terminal := store.UpsertActor(Actor{
		ActorKey:          "terminal-wechat",
		Token:             "terminal-token",
		Kind:              ActorKindTerminal,
		Channel:           "wechat",
		ProviderAccountID: "wx-account-1",
	})
	ai := store.UpsertActor(Actor{
		ActorKey:    "aiagent-clawman",
		Token:       "ai-token",
		Kind:        ActorKindAIAgent,
		Permissions: []string{PermissionSubscribeAll},
	})

	if _, _, err := store.AppendTerminalMessage(ctx, terminal, SendMessageRequest{
		Room: RoomInput{
			Channel:           "wechat",
			ProviderAccountID: "wx-account-1",
			ExternalRoomID:    "alice",
		},
		ExternalMessageID: "wechat-message-1",
		SenderID:          "alice",
		MessageType:       "text",
		Body:              json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("append terminal message: %v", err)
	}
	updates, err := store.ListUpdates(ctx, ai, 0, 10)
	if err != nil {
		t.Fatalf("list ai updates: %v", err)
	}
	var payload struct {
		ContextToken string `json:"context_token"`
	}
	if err := json.Unmarshal(updates[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	task, err := store.CreateReplyTask(ctx, ai, SendMessageRequest{
		ContextToken: payload.ContextToken,
		MessageType:  "text",
		Body:         json.RawMessage(`{"text":"pong"}`),
	})
	if err != nil {
		t.Fatalf("create reply task: %v", err)
	}
	if _, err := store.Ack(ctx, terminal, AckRequest{TaskResults: []TaskResultInput{{
		TaskID: task.ID,
		Status: TaskStatusAcked,
	}}}); err != nil {
		t.Fatalf("ack task: %v", err)
	}
	if _, err := store.Ack(ctx, terminal, AckRequest{TaskResults: []TaskResultInput{{
		TaskID: task.ID,
		Status: TaskStatusFailed,
		Error:  "late failure",
	}}}); err == nil {
		t.Fatal("conflicting terminal task status should fail")
	}
	if got := store.tasks[task.ID].Status; got != TaskStatusAcked {
		t.Fatalf("task status = %q, want %q", got, TaskStatusAcked)
	}
}

func TestSubscribedTerminalActorDoesNotReceiveMessageReceived(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	terminal := store.UpsertActor(Actor{
		ActorKey:          "terminal-wechat",
		Token:             "terminal-token",
		Kind:              ActorKindTerminal,
		Channel:           "wechat",
		ProviderAccountID: "wx-account-1",
	})
	first, _, err := store.AppendTerminalMessage(ctx, terminal, SendMessageRequest{
		Room: RoomInput{
			Channel:           "wechat",
			ProviderAccountID: "wx-account-1",
			ExternalRoomID:    "alice",
		},
		ExternalMessageID: "wechat-message-1",
		SenderID:          "alice",
		MessageType:       "text",
		Body:              json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("append first terminal message: %v", err)
	}
	store.SubscribeActorToRoom(terminal.ID, first.RoomID)
	if _, _, err := store.AppendTerminalMessage(ctx, terminal, SendMessageRequest{
		Room: RoomInput{
			Channel:           "wechat",
			ProviderAccountID: "wx-account-1",
			ExternalRoomID:    "alice",
		},
		ExternalMessageID: "wechat-message-2",
		SenderID:          "alice",
		MessageType:       "text",
		Body:              json.RawMessage(`{"text":"hello again"}`),
	}); err != nil {
		t.Fatalf("append second terminal message: %v", err)
	}
	updates, err := store.ListUpdates(ctx, terminal, 0, 10)
	if err != nil {
		t.Fatalf("list terminal updates: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("terminal updates = %+v, want none", updates)
	}
}

func postJSON(t *testing.T, url string, token string, body any, out any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		t.Fatalf("post %s status = %d error = %+v", url, resp.StatusCode, apiErr)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
}
