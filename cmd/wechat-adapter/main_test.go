package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCursorAcceptsMessagesWithoutLocalIDAtSameTimestamp(t *testing.T) {
	messages := []wxMessage{
		{Username: "测试群", Timestamp: 100, Content: "first"},
		{Username: "测试群", Timestamp: 100, Content: "second"},
	}
	c := cursor{}
	for _, message := range messages {
		if !c.accepts(message) {
			t.Fatalf("cursor rejected %q at same timestamp", message.Content)
		}
		c = c.advance(message)
	}
	if c.accepts(messages[0]) {
		t.Fatalf("cursor accepted duplicate message")
	}
}

func TestAdapterTargetMatchesGroupNameOrID(t *testing.T) {
	a := adapter{cfg: config{GroupID: "123@chatroom", GroupName: "测试群"}}
	tests := []struct {
		name string
		msg  wxMessage
		want bool
	}{
		{name: "id", msg: wxMessage{Username: "123@chatroom", Chat: "other"}, want: true},
		{name: "name", msg: wxMessage{Username: "456@chatroom", Chat: "测试群"}, want: true},
		{name: "other", msg: wxMessage{Username: "456@chatroom", Chat: "别的群"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.isTargetMessage(tt.msg); got != tt.want {
				t.Fatalf("isTargetMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeWechatType(t *testing.T) {
	if got := normalizeWechatType("图片"); got != "image" {
		t.Fatalf("image type = %q", got)
	}
	if got := normalizeWechatType("文本"); got != "text" {
		t.Fatalf("text type = %q", got)
	}
	if got := normalizeWechatType("系统"); got != "system" {
		t.Fatalf("system type = %q", got)
	}
}

func TestShouldSkipMessage(t *testing.T) {
	a := adapter{cfg: config{SelfSenders: map[string]bool{"私云虾虾": true}}}
	if a.shouldSkipMessage(wxMessage{Type: "文本", Sender: "小金鱼"}) {
		t.Fatalf("text message should not be skipped")
	}
	if a.shouldSkipMessage(wxMessage{Type: "图片", Sender: "小金鱼"}) {
		t.Fatalf("image message should not be skipped")
	}
	if !a.shouldSkipMessage(wxMessage{Type: "系统", Sender: "小金鱼"}) {
		t.Fatalf("system message should be skipped")
	}
	if !a.shouldSkipMessage(wxMessage{Type: "文本", Sender: "私云虾虾"}) {
		t.Fatalf("self text message should be skipped")
	}
	if !a.shouldSkipMessage(wxMessage{Type: "文本", Sender: ""}) {
		t.Fatalf("empty sender message should be skipped")
	}
}

func TestCreateMessagePostsClawmanPayload(t *testing.T) {
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/messages" {
			t.Fatalf("path = %q, want /api/messages", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"message":{"id":1}}`))
	}))
	defer server.Close()

	a := adapter{
		cfg: config{
			ClawmanBaseURL: server.URL,
			ClawmanToken:   "token",
			GroupID:        defaultWechatGroupID,
			GroupName:      defaultWechatGroup,
			SelfSenders:    map[string]bool{"私云虾虾": true},
		},
		client: server.Client(),
		roomID: 10,
	}
	err := a.createMessage(t.Context(), wxMessage{
		Username:  defaultWechatGroupID,
		Chat:      defaultWechatGroup,
		LocalID:   7,
		Sender:    "小金鱼",
		Timestamp: 1779640234,
		Type:      "文本",
		Content:   "虾虾，你好呀",
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	if posted["source_message_id"] != "wechat:50261801724@chatroom:7" {
		t.Fatalf("source_message_id = %v", posted["source_message_id"])
	}
	if posted["skipped"] != false {
		t.Fatalf("skipped = %v, want false", posted["skipped"])
	}
	payload := posted["payload"].(map[string]any)
	if payload["type"] != "text" || payload["text"] != "虾虾，你好呀" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestCreateImageMessageUsesStableImagePayload(t *testing.T) {
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"message":{"id":1}}`))
	}))
	defer server.Close()

	a := adapter{
		cfg: config{
			ClawmanBaseURL: server.URL,
			ClawmanToken:   "token",
			GroupID:        defaultWechatGroupID,
			GroupName:      defaultWechatGroup,
			SelfSenders:    map[string]bool{"私云虾虾": true},
		},
		client: server.Client(),
		roomID: 10,
	}
	msg := normalizeWXMessage(wxMessage{
		Username:  defaultWechatGroupID,
		Chat:      defaultWechatGroup,
		Sender:    "小金鱼",
		Timestamp: 1779640234,
		Type:      "图片",
		Content:   "[图片] local_id=20281",
	})
	if err := a.createMessage(t.Context(), msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if posted["source_message_id"] != "wechat:50261801724@chatroom:20281" {
		t.Fatalf("source_message_id = %v", posted["source_message_id"])
	}
	if posted["skipped"] != false {
		t.Fatalf("skipped = %v, want false", posted["skipped"])
	}
	payload := posted["payload"].(map[string]any)
	if payload["type"] != "image" || payload["text"] != "[图片]" || payload["raw_text"] != "[图片] local_id=20281" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSourceMessageIDFallbackIncludesSenderTypeTimeAndContent(t *testing.T) {
	first := wxMessage{
		Username:  defaultWechatGroupID,
		Sender:    "小金鱼",
		Timestamp: 1779640234,
		Time:      "2026-05-25 00:30",
		Type:      "文本",
		Content:   "same",
	}
	second := first
	second.Sender = "另一个人"
	if sourceMessageID(first) == sourceMessageID(second) {
		t.Fatalf("source ids should differ for same timestamp/content from different senders")
	}
}

func TestRunOnceReadsWxAndPostsTargetMessages(t *testing.T) {
	tempDir := t.TempDir()
	wxPath := filepath.Join(tempDir, "wx")
	wxOutput := `[
  {
    "chat": "测试群",
    "chat_type": "group",
    "content": "虾虾，你好呀",
    "is_group": true,
    "local_id": 7,
    "sender": "小金鱼",
    "time": "2026-05-25 00:30",
    "timestamp": 1779640234,
    "type": "文本",
    "username": "50261801724@chatroom"
  },
  {
    "chat": "别的群",
    "chat_type": "group",
    "content": "ignore",
    "is_group": true,
    "local_id": 9,
    "sender": "someone",
    "time": "2026-05-25 00:31",
    "timestamp": 1779640300,
    "type": "文本",
    "username": "other@chatroom"
  }
]`
	if err := os.WriteFile(wxPath, []byte("#!/bin/sh\nprintf '%s' '"+wxOutput+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake wx: %v", err)
	}

	var roomCalls int
	var messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/rooms":
			roomCalls++
			_, _ = w.Write([]byte(`{"room":{"id":42},"agent_session":{"id":1}}`))
		case "/api/messages":
			messageCalls++
			var posted map[string]any
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode message: %v", err)
			}
			if posted["room_id"] != float64(42) {
				t.Fatalf("room_id = %v", posted["room_id"])
			}
			if posted["source_message_id"] != "wechat:50261801724@chatroom:7" {
				t.Fatalf("source_message_id = %v", posted["source_message_id"])
			}
			_, _ = w.Write([]byte(`{"message":{"id":1}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	t.Setenv("CLAWMAN_API_TOKEN", "token")
	t.Setenv("CLAWMAN_BASE_URL", server.URL)
	t.Setenv("WECHAT_WX_BIN", wxPath)
	t.Setenv("WECHAT_GROUP_ID", defaultWechatGroupID)
	t.Setenv("WECHAT_GROUP_NAME", defaultWechatGroup)
	t.Setenv("WECHAT_READ_MODE", "history")
	t.Setenv("WECHAT_CURSOR_PATH", filepath.Join(tempDir, "cursor.json"))
	t.Setenv("WECHAT_ONCE", "true")
	t.Setenv("WECHAT_SELF_SENDERS", "私云虾虾")

	if err := run(t.Context()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if roomCalls != 1 || messageCalls != 1 {
		t.Fatalf("roomCalls=%d messageCalls=%d, want 1/1", roomCalls, messageCalls)
	}
}

func TestRunOnceDoesNotPostDuplicateMessageInSameBatch(t *testing.T) {
	tempDir := t.TempDir()
	wxPath := filepath.Join(tempDir, "wx")
	wxOutput := `[
  {
    "chat": "测试群",
    "chat_type": "group",
    "content": "虾虾，你好呀",
    "is_group": true,
    "local_id": 7,
    "sender": "小金鱼",
    "time": "2026-05-25 00:30",
    "timestamp": 1779640234,
    "type": "文本",
    "username": "50261801724@chatroom"
  },
  {
    "chat": "测试群",
    "chat_type": "group",
    "content": "虾虾，你好呀",
    "is_group": true,
    "local_id": 7,
    "sender": "小金鱼",
    "time": "2026-05-25 00:30",
    "timestamp": 1779640234,
    "type": "文本",
    "username": "50261801724@chatroom"
  }
]`
	if err := os.WriteFile(wxPath, []byte("#!/bin/sh\nprintf '%s' '"+wxOutput+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake wx: %v", err)
	}

	var messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/rooms":
			_, _ = w.Write([]byte(`{"room":{"id":42},"agent_session":{"id":1}}`))
		case "/api/messages":
			messageCalls++
			_, _ = w.Write([]byte(`{"message":{"id":1}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	t.Setenv("CLAWMAN_API_TOKEN", "token")
	t.Setenv("CLAWMAN_BASE_URL", server.URL)
	t.Setenv("WECHAT_WX_BIN", wxPath)
	t.Setenv("WECHAT_GROUP_ID", defaultWechatGroupID)
	t.Setenv("WECHAT_GROUP_NAME", defaultWechatGroup)
	t.Setenv("WECHAT_READ_MODE", "history")
	t.Setenv("WECHAT_CURSOR_PATH", filepath.Join(tempDir, "cursor.json"))
	t.Setenv("WECHAT_ONCE", "true")

	if err := run(t.Context()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if messageCalls != 1 {
		t.Fatalf("messageCalls=%d, want 1", messageCalls)
	}
}

func TestRunOnceSkipsSelfMessages(t *testing.T) {
	tempDir := t.TempDir()
	wxPath := filepath.Join(tempDir, "wx")
	wxOutput := `[{
  "chat": "测试群",
  "chat_type": "group",
  "content": "虾虾回复",
  "is_group": true,
  "local_id": 8,
  "sender": "私云虾虾",
  "time": "2026-05-25 00:32",
  "timestamp": 1779640320,
  "type": "文本",
  "username": "50261801724@chatroom"
}]`
	if err := os.WriteFile(wxPath, []byte("#!/bin/sh\nprintf '%s' '"+wxOutput+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake wx: %v", err)
	}

	var messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/rooms":
			_, _ = w.Write([]byte(`{"room":{"id":42},"agent_session":{"id":1}}`))
		case "/api/messages":
			messageCalls++
			var posted map[string]any
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode message: %v", err)
			}
			if posted["skipped"] != true {
				t.Fatalf("skipped = %v, want true for self message", posted["skipped"])
			}
			_, _ = w.Write([]byte(`{"message":{"id":1}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	t.Setenv("CLAWMAN_API_TOKEN", "token")
	t.Setenv("CLAWMAN_BASE_URL", server.URL)
	t.Setenv("WECHAT_WX_BIN", wxPath)
	t.Setenv("WECHAT_READ_MODE", "history")
	t.Setenv("WECHAT_CURSOR_PATH", filepath.Join(tempDir, "cursor.json"))
	t.Setenv("WECHAT_ONCE", "true")
	t.Setenv("WECHAT_SELF_SENDERS", "私云虾虾")

	if err := run(t.Context()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if messageCalls != 1 {
		t.Fatalf("messageCalls=%d, want 1 skipped message persisted", messageCalls)
	}
}

func TestReadHistoryMessagesAddsTargetRoomIdentity(t *testing.T) {
	tempDir := t.TempDir()
	wxPath := filepath.Join(tempDir, "wx")
	wxOutput := `[{
  "content": "虾虾，你好呀",
  "local_id": 7,
  "sender": "小金鱼",
  "time": "2026-05-25 00:30",
  "timestamp": 1779640234,
  "type": "文本"
}]`
	if err := os.WriteFile(wxPath, []byte("#!/bin/sh\nprintf '%s' '"+wxOutput+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake wx: %v", err)
	}

	a := adapter{cfg: config{
		WXBin:     wxPath,
		GroupID:   defaultWechatGroupID,
		GroupName: defaultWechatGroup,
		PollLimit: 10,
		ReadMode:  "history",
	}}
	messages, err := a.readNewMessages(t.Context())
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].Username != defaultWechatGroupID || messages[0].Chat != defaultWechatGroup || messages[0].ChatType != "group" || !messages[0].IsGroup {
		t.Fatalf("message room identity = %+v", messages[0])
	}
}

func TestReadAutoFallsBackToNewMessagesWhenHistoryFails(t *testing.T) {
	tempDir := t.TempDir()
	wxPath := filepath.Join(tempDir, "wx")
	wxOutput := `[{
  "chat": "测试群",
  "chat_type": "group",
  "content": "虾虾，你好呀",
  "is_group": true,
  "local_id": 7,
  "sender": "小金鱼",
  "time": "2026-05-25 00:30",
  "timestamp": 1779640234,
  "type": "文本",
  "username": "50261801724@chatroom"
}]`
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"history\" ]; then echo 'not found' >&2; exit 1; fi\n" +
		"if [ \"$1\" = \"new-messages\" ]; then printf '%s' '" + wxOutput + "'; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(wxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake wx: %v", err)
	}

	a := adapter{cfg: config{
		WXBin:     wxPath,
		GroupID:   defaultWechatGroupID,
		GroupName: defaultWechatGroup,
		PollLimit: 10,
		ReadMode:  "auto",
	}}
	messages, err := a.readNewMessages(t.Context())
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Username != defaultWechatGroupID {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestHistoryChatCandidatesPreferOverrideThenNameThenID(t *testing.T) {
	a := adapter{cfg: config{
		GroupID:     "123@chatroom",
		GroupName:   "测试群",
		HistoryChat: "wx-history-key",
	}}
	got := a.historyChatCandidates()
	want := []string{"wx-history-key", "测试群", "123@chatroom"}
	if len(got) != len(want) {
		t.Fatalf("candidates len = %d, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("candidate[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
