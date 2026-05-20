package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

func writeJSONTest(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
