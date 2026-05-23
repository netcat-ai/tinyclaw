package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

func TestResolveGroupDisplayNameUsesArchiveSecret(t *testing.T) {
	var tokenRequests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			secret := r.URL.Query().Get("corpsecret")
			tokenRequests = append(tokenRequests, secret)
			writeJSONTest(t, w, map[string]any{
				"errcode":      0,
				"access_token": "token-for-" + secret,
				"expires_in":   7200,
			})
		case "/cgi-bin/msgaudit/groupchat/get":
			if got := r.URL.Query().Get("access_token"); got != "token-for-archive-secret" {
				t.Fatalf("access_token = %q, want archive token", got)
			}
			writeJSONTest(t, w, map[string]any{
				"errcode":  0,
				"roomname": "Archive Group",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	SetBaseURL(server.URL)
	defer ResetBaseURL()

	adapter := NewArchiveAdapter(nil, nil, ArchiveConfig{
		CorpID:        "corp-id",
		CorpSecret:    "archive-secret",
		ContactSecret: "contact-secret",
	})

	name := adapter.resolveRoomDisplayName(context.Background(), core.RoomChatTypeGroup, "room-id")
	if name != "Archive Group" {
		t.Fatalf("name = %q, want Archive Group", name)
	}
	if len(tokenRequests) != 1 || tokenRequests[0] != "archive-secret" {
		t.Fatalf("token requests = %v, want only archive-secret", tokenRequests)
	}
}

func TestResolveGroupDisplayNameFallsBackToExternalGroupSecret(t *testing.T) {
	var groupChatToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			secret := r.URL.Query().Get("corpsecret")
			writeJSONTest(t, w, map[string]any{
				"errcode":      0,
				"access_token": "token-for-" + secret,
				"expires_in":   7200,
			})
		case "/cgi-bin/msgaudit/groupchat/get":
			writeJSONTest(t, w, map[string]any{
				"errcode": 301059,
				"errmsg":  "only support inner room",
			})
		case "/cgi-bin/externalcontact/groupchat/get":
			groupChatToken = r.URL.Query().Get("access_token")
			writeJSONTest(t, w, map[string]any{
				"errcode": 0,
				"group_chat": map[string]any{
					"chat_id": "external-room-id",
					"name":    "External Group",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	SetBaseURL(server.URL)
	defer ResetBaseURL()

	adapter := NewArchiveAdapter(nil, nil, ArchiveConfig{
		CorpID:        "corp-id",
		CorpSecret:    "archive-secret",
		ContactSecret: "contact-secret",
	})

	name := adapter.resolveRoomDisplayName(context.Background(), core.RoomChatTypeGroup, "external-room-id")
	if name != "External Group" {
		t.Fatalf("name = %q, want External Group", name)
	}
	if groupChatToken != "token-for-contact-secret" {
		t.Fatalf("group chat token = %q, want contact token", groupChatToken)
	}
}

func TestArchiveAdapterIngestsMessageThroughMessageIngestor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			writeJSONTest(t, w, map[string]any{
				"errcode":      0,
				"access_token": "token",
				"expires_in":   7200,
			})
		case "/cgi-bin/externalcontact/get":
			writeJSONTest(t, w, map[string]any{
				"errcode": 0,
				"external_contact": map[string]any{
					"external_userid": "user-1",
					"name":            "User One",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	SetBaseURL(server.URL)
	defer ResetBaseURL()

	store := &fakeArchiveStore{}
	messages := &fakeArchiveMessageIngestor{}
	adapter := NewArchiveAdapter(nil, store, ArchiveConfig{
		CorpID:     "corp-id",
		CorpSecret: "corp-secret",
		BotID:      "bot-id",
	})
	adapter.SetMessageIngestor(messages)

	err := adapter.ingestArchiveMessage(context.Background(), 42, archiveMessage{
		MsgID:   "msg-1",
		Action:  "send",
		From:    "user-1",
		ToList:  []string{"bot-id"},
		MsgTime: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC).UnixMilli(),
		MsgType: "text",
		Text: struct {
			Content string `json:"content"`
		}{Content: "/draw 一朵花"},
	}, json.RawMessage(`{"msgid":"msg-1"}`))
	if err != nil {
		t.Fatalf("ingest archive message: %v", err)
	}
	if store.createCalls != 0 {
		t.Fatalf("store create calls=%d, want 0", store.createCalls)
	}
	if messages.calls != 1 {
		t.Fatalf("message ingestor calls=%d, want 1", messages.calls)
	}
	if string(messages.input.Payload) == "" || messages.input.RoomID != 99 {
		t.Fatalf("message input = %+v", messages.input)
	}
}

type fakeArchiveStore struct {
	createCalls int
}

func (s *fakeArchiveStore) RegisterRoom(context.Context, core.RegisterRoomInput) (core.RegisterRoomResult, error) {
	return core.RegisterRoomResult{Room: core.Room{ID: 99}}, nil
}

func (s *fakeArchiveStore) CreateMessage(context.Context, core.CreateMessageInput) (core.CreateMessageResult, error) {
	s.createCalls++
	return core.CreateMessageResult{}, nil
}

type fakeArchiveMessageIngestor struct {
	calls int
	input core.CreateMessageInput
}

func (i *fakeArchiveMessageIngestor) IngestMessage(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	i.calls++
	i.input = input
	return core.CreateMessageResult{Message: core.Message{ID: 123, Payload: input.Payload}}, nil
}

func writeJSONTest(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
