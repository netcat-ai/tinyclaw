package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterClientInvoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat" {
			t.Fatalf("path = %s, want /v1/chat", r.URL.Path)
		}
		if got := r.Header.Get("X-Sandbox-ID"); got != "clawagent-room-1" {
			t.Fatalf("X-Sandbox-ID = %q, want %q", got, "clawagent-room-1")
		}
		if got := r.Header.Get("X-Sandbox-Namespace"); got != "claw" {
			t.Fatalf("X-Sandbox-Namespace = %q, want %q", got, "claw")
		}
		if got := r.Header.Get("X-Sandbox-Port"); got != "8888" {
			t.Fatalf("X-Sandbox-Port = %q, want %q", got, "8888")
		}

		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Text != "hello" {
			t.Fatalf("request text = %q, want %q", req.Text, "hello")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{Text: "sandbox reply"})
	}))
	defer server.Close()

	client := NewRouterClient(server.Client(), RouterConfig{
		BaseURL:    server.URL,
		Namespace:  "claw",
		ServerPort: 8888,
	})

	resp, err := client.Invoke(context.Background(), "clawagent-room-1", ChatRequest{
		MsgID:    "msg-1",
		RoomID:   "room-1",
		TenantID: "corp-id",
		ChatType: "group",
		Text:     "hello",
	})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if resp.Text != "sandbox reply" {
		t.Fatalf("response text = %q, want %q", resp.Text, "sandbox reply")
	}
}

func TestRouterClientInvoke_PropagatesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sandbox failed", http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewRouterClient(server.Client(), RouterConfig{
		BaseURL:    server.URL,
		Namespace:  "claw",
		ServerPort: 8888,
	})

	_, err := client.Invoke(context.Background(), "clawagent-room-1", ChatRequest{
		MsgID:    "msg-1",
		RoomID:   "room-1",
		TenantID: "corp-id",
		ChatType: "group",
		Text:     "hello",
	})
	if err == nil {
		t.Fatal("Invoke error = nil, want non-nil")
	}
}
