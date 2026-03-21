package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tinyclaw/sandbox"
	"tinyclaw/wecom"
	"tinyclaw/wecom/finance"
)

func newTestClawman(t *testing.T, serverURL string) *Clawman {
	t.Helper()

	wecom.SetBaseURL(serverURL)
	t.Cleanup(wecom.ResetBaseURL)

	return &Clawman{
		contactAPI: wecom.NewClient("corp-id", "contact-secret"),
		archiveAPI: wecom.NewClient("corp-id", "corp-secret"),
		cache:      newTTLCache(),
	}
}

func newWeComTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"errcode":      0,
			"errmsg":       "ok",
			"access_token": "token",
			"expires_in":   7200,
		})
	})
	mux.HandleFunc("/", handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}

func TestResolveExternalContactUsesCustomerAPIAndCaches(t *testing.T) {
	var externalCalls int
	var userCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/externalcontact/get":
			externalCalls++
			if got := r.URL.Query().Get("external_userid"); got != "wm123" {
				t.Fatalf("external_userid = %q, want wm123", got)
			}
			writeJSON(t, w, map[string]any{
				"errcode": 0,
				"errmsg":  "ok",
				"external_contact": map[string]any{
					"external_userid": "wm123",
					"name":            "外部联系人A",
					"corp_name":       "Acme",
				},
			})
		case "/cgi-bin/user/get":
			userCalls++
			writeJSON(t, w, map[string]any{"errcode": 0, "errmsg": "ok", "userid": "ignored", "name": "ignored"})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	first, err := clawman.Resolve(ctx, "wm123")
	if err != nil {
		t.Fatalf("Resolve first call: %v", err)
	}
	second, err := clawman.Resolve(ctx, "wm123")
	if err != nil {
		t.Fatalf("Resolve second call: %v", err)
	}

	if first.Type != "external" || first.Name != "外部联系人A" {
		t.Fatalf("first resolve = %+v", first)
	}
	if second.Type != "external" || second.Name != "外部联系人A" {
		t.Fatalf("second resolve = %+v", second)
	}
	if externalCalls != 1 {
		t.Fatalf("external contact API calls = %d, want 1", externalCalls)
	}
	if userCalls != 0 {
		t.Fatalf("internal user API calls = %d, want 0", userCalls)
	}
}

func TestResolveInternalUserUsesUserAPI(t *testing.T) {
	var externalCalls int
	var userCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/externalcontact/get":
			externalCalls++
			writeJSON(t, w, map[string]any{"errcode": 0, "errmsg": "ok"})
		case "/cgi-bin/user/get":
			userCalls++
			if got := r.URL.Query().Get("userid"); got != "zhangsan" {
				t.Fatalf("userid = %q, want zhangsan", got)
			}
			writeJSON(t, w, map[string]any{
				"errcode": 0,
				"errmsg":  "ok",
				"userid":  "zhangsan",
				"name":    "张三",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	ident, err := clawman.Resolve(ctx, "zhangsan")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if ident.Type != "employee" || ident.Name != "张三" || ident.UserID != "zhangsan" {
		t.Fatalf("resolve = %+v", ident)
	}
	if userCalls != 1 {
		t.Fatalf("internal user API calls = %d, want 1", userCalls)
	}
	if externalCalls != 0 {
		t.Fatalf("external contact API calls = %d, want 0", externalCalls)
	}
}

func TestResolveFailureDoesNotCachePlaceholder(t *testing.T) {
	var externalCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/externalcontact/get":
			externalCalls++
			writeJSON(t, w, map[string]any{
				"errcode": 40001,
				"errmsg":  "temporary failure",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	if _, err := clawman.Resolve(ctx, "wm123"); err == nil {
		t.Fatal("Resolve error = nil, want non-nil")
	}
	if _, err := clawman.Resolve(ctx, "wm123"); err == nil {
		t.Fatal("Resolve second error = nil, want non-nil")
	}
	if externalCalls != 2 {
		t.Fatalf("external contact API calls = %d, want 2", externalCalls)
	}
}

func TestPrimeSenderIdentityUsesResolveCache(t *testing.T) {
	var userCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/user/get":
			userCalls++
			if got := r.URL.Query().Get("userid"); got != "zhangsan" {
				t.Fatalf("userid = %q, want zhangsan", got)
			}
			writeJSON(t, w, map[string]any{
				"errcode": 0,
				"errmsg":  "ok",
				"userid":  "zhangsan",
				"name":    "张三",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()
	msg := &WeComMessage{MsgID: "msg-1", From: "zhangsan"}

	if ok := clawman.primeSenderIdentity(ctx, msg); !ok {
		t.Fatal("primeSenderIdentity first call = false, want true")
	}
	if ok := clawman.primeSenderIdentity(ctx, msg); !ok {
		t.Fatal("primeSenderIdentity second call = false, want true")
	}
	if userCalls != 1 {
		t.Fatalf("internal user API calls = %d, want 1", userCalls)
	}
}

func TestResolveRoutingTargetUsesGroupNameForGroupMessage(t *testing.T) {
	var archiveGroupCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/msgaudit/groupchat/get":
			archiveGroupCalls++
			writeJSON(t, w, map[string]any{
				"errcode":  0,
				"errmsg":   "ok",
				"roomname": "研发群",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()
	msg := &WeComMessage{
		MsgID:  "msg-ctx-1",
		From:   "zhangsan",
		RoomID: "room-internal",
	}

	targetName, err := clawman.resolveRoutingTarget(ctx, msg, &Identity{
		UserID: "zhangsan",
		Name:   "张三",
		Type:   "employee",
	})
	if err != nil {
		t.Fatalf("resolveRoutingTarget: %v", err)
	}

	if targetName != "研发群" {
		t.Fatalf("targetName = %q, want %q", targetName, "研发群")
	}
	if archiveGroupCalls != 1 {
		t.Fatalf("archive group API calls = %d, want 1", archiveGroupCalls)
	}
}

func TestResolveRoutingTargetUsesSenderNameForDirectMessage(t *testing.T) {
	clawman := &Clawman{}
	msg := &WeComMessage{
		MsgID: "msg-direct-1",
		From:  "zhangsan",
	}

	targetName, err := clawman.resolveRoutingTarget(context.Background(), msg, &Identity{
		UserID: "zhangsan",
		Name:   "张三",
		Type:   "employee",
	})
	if err != nil {
		t.Fatalf("resolveRoutingTarget: %v", err)
	}

	if targetName != "张三" {
		t.Fatalf("targetName = %q, want %q", targetName, "张三")
	}
}

func TestShouldSkipArchivedMessageSkipsBotSelfMessageInDirectChat(t *testing.T) {
	clawman := &Clawman{
		cfg: Config{
			WeComBotID: "moss",
		},
	}

	if !clawman.shouldSkipArchivedMessage(&WeComMessage{From: "moss"}) {
		t.Fatal("shouldSkipArchivedMessage = false, want true")
	}
}

func TestShouldSkipArchivedMessageSkipsBotSelfMessageInGroupChat(t *testing.T) {
	clawman := &Clawman{
		cfg: Config{
			WeComBotID: "moss",
		},
	}

	if !clawman.shouldSkipArchivedMessage(&WeComMessage{
		From:   "moss",
		RoomID: "wrg-oKJwAANVxkGsVgVraqwm3SH6GWSw",
	}) {
		t.Fatal("shouldSkipArchivedMessage = false, want true")
	}
}

func TestShouldSkipArchivedMessageDoesNotSkipHumanGroupMessage(t *testing.T) {
	clawman := &Clawman{
		cfg: Config{
			WeComBotID: "moss",
		},
	}

	if clawman.shouldSkipArchivedMessage(&WeComMessage{
		From:   "wmg-oKJwAAdttdPCnGMd1F5ryrvItWCg",
		RoomID: "wrg-oKJwAANVxkGsVgVraqwm3SH6GWSw",
	}) {
		t.Fatal("shouldSkipArchivedMessage = true, want false")
	}
}

func TestStatusForMessageDirectChatAlwaysPending(t *testing.T) {
	clawman := &Clawman{}

	status, promote, err := clawman.statusForMessage(&WeComMessage{From: "zhangsan"}, `{"msgtype":"text","text":{"content":"hello"}}`)
	if err != nil {
		t.Fatalf("statusForMessage error: %v", err)
	}
	if status != statusPending {
		t.Fatalf("status = %q, want %q", status, statusPending)
	}
	if promote {
		t.Fatal("promote = true, want false")
	}
}

func TestStatusForMessageGroupRequiresMentionOrKeyword(t *testing.T) {
	clawman := &Clawman{
		groupTriggerKeywords: normalizeTriggerTerms([]string{"tinyclaw"}),
		groupMentionPattern:  buildGroupMentionPattern([]string{"moss", "tiny"}),
	}

	tests := []struct {
		name    string
		text    string
		want    string
		promote bool
	}{
		{
			name:    "group message without trigger",
			text:    "大家下午好",
			want:    statusBuffered,
			promote: false,
		},
		{
			name:    "group message with mention",
			text:    "请 @moss 看一下",
			want:    statusPending,
			promote: true,
		},
		{
			name:    "group message with keyword",
			text:    "tinyclaw 帮我总结一下",
			want:    statusPending,
			promote: true,
		},
		{
			name:    "mention should not match partial suffix",
			text:    "请 @mossbot 看一下",
			want:    statusBuffered,
			promote: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, promote, err := clawman.statusForMessage(&WeComMessage{
				From:   "zhangsan",
				RoomID: "room-1",
			}, `{"msgtype":"text","text":{"content":"`+tc.text+`"}}`)
			if err != nil {
				t.Fatalf("statusForMessage error: %v", err)
			}
			if status != tc.want {
				t.Fatalf("statusForMessage(%q) status = %q, want %q", tc.text, status, tc.want)
			}
			if promote != tc.promote {
				t.Fatalf("statusForMessage(%q) promote = %v, want %v", tc.text, promote, tc.promote)
			}
		})
	}
}

func TestBuildAgentMessages(t *testing.T) {
	got := buildAgentMessages([]MessageRecord{
		{
			Seq:       1,
			MsgID:     "msg-1",
			FromID:    "zhangsan",
			FromName:  "张三",
			Payload:   `{"msgtype":"text","text":{"content":"第一句"}}`,
			MsgTime:   time.Date(2026, 3, 19, 8, 0, 0, 0, time.UTC),
			CreatedAt: time.Date(2026, 3, 19, 8, 0, 0, 0, time.UTC),
		},
		{
			Seq:       2,
			MsgID:     "msg-2",
			FromID:    "lisi",
			Payload:   `{"msgtype":"text","text":{"content":"第二句"}}`,
			MsgTime:   time.Date(2026, 3, 19, 8, 1, 0, 0, time.UTC),
			CreatedAt: time.Date(2026, 3, 19, 8, 1, 0, 0, time.UTC),
		},
	})
	want := []sandbox.AgentMessage{
		{
			Seq:      1,
			MsgID:    "msg-1",
			FromID:   "zhangsan",
			FromName: "张三",
			MsgTime:  "2026-03-19T08:00:00Z",
			Payload:  `{"msgtype":"text","text":{"content":"第一句"}}`,
		},
		{
			Seq:      2,
			MsgID:    "msg-2",
			FromID:   "lisi",
			FromName: "",
			MsgTime:  "2026-03-19T08:01:00Z",
			Payload:  `{"msgtype":"text","text":{"content":"第二句"}}`,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("len(buildAgentMessages) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("buildAgentMessages[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildMessageRecordReturnsFatalErrorOnDecryptFailure(t *testing.T) {
	clawman := &Clawman{
		cfg: Config{
			WeComCorpID: "corp-id",
		},
	}

	_, _, err := clawman.buildMessageRecord(context.Background(), finance.ChatData{
		Seq:   1,
		MsgID: "msg-decrypt-fail",
	})
	if err == nil {
		t.Fatal("buildMessageRecord error = nil, want non-nil")
	}
	if !errors.Is(err, errFatalIngest) {
		t.Fatalf("buildMessageRecord error = %v, want fatal ingest error", err)
	}
}

func TestValidateParsedMessage(t *testing.T) {
	tests := []struct {
		name    string
		msg     *WeComMessage
		wantErr bool
	}{
		{
			name: "valid message",
			msg: &WeComMessage{
				MsgID:  "msg-1",
				From:   "zhangsan",
				ToList: []string{"bot"},
			},
			wantErr: false,
		},
		{
			name: "missing msgid",
			msg: &WeComMessage{
				From:   "zhangsan",
				ToList: []string{"bot"},
			},
			wantErr: true,
		},
		{
			name: "missing from",
			msg: &WeComMessage{
				ToList: []string{"bot"},
			},
			wantErr: true,
		},
		{
			name: "missing tolist",
			msg: &WeComMessage{
				From: "zhangsan",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateParsedMessage(tc.msg)
			if tc.wantErr && err == nil {
				t.Fatal("validateParsedMessage error = nil, want non-nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateParsedMessage error = %v, want nil", err)
			}
		})
	}
}

func TestPrimeSenderIdentityFailureReturnsFalse(t *testing.T) {
	var externalCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/externalcontact/get":
			externalCalls++
			writeJSON(t, w, map[string]any{
				"errcode": 40001,
				"errmsg":  "temporary failure",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()
	msg := &WeComMessage{MsgID: "msg-2", From: "wm123"}

	if ok := clawman.primeSenderIdentity(ctx, msg); ok {
		t.Fatal("primeSenderIdentity = true, want false")
	}
	if ok := clawman.primeSenderIdentity(ctx, msg); ok {
		t.Fatal("primeSenderIdentity second call = true, want false")
	}
	if externalCalls != 1 {
		t.Fatalf("external contact API calls = %d, want 1", externalCalls)
	}
	if !clawman.cache.Has(primeSenderFailCachePrefix + "wm123") {
		t.Fatal("prime sender failure should be cached in memory")
	}
}

func TestResolveGroupUsesArchiveFirstThenFallsBackToCustomerGroup(t *testing.T) {
	var customerGroupCalls int
	var archiveGroupCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/msgaudit/groupchat/get":
			archiveGroupCalls++
			req := struct {
				RoomID string `json:"roomid"`
			}{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode archive groupchat request: %v", err)
			}
			if req.RoomID == "room-internal" {
				writeJSON(t, w, map[string]any{
					"errcode":  0,
					"errmsg":   "ok",
					"roomname": "内部群B",
				})
				return
			}
			writeJSON(t, w, map[string]any{
				"errcode": 40001,
				"errmsg":  "not archive group",
			})
		case "/cgi-bin/externalcontact/groupchat/get":
			customerGroupCalls++
			req := struct {
				ChatID string `json:"chat_id"`
			}{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode customer groupchat request: %v", err)
			}
			if req.ChatID != "room-customer" {
				t.Fatalf("chat_id = %q, want room-customer", req.ChatID)
			}
			writeJSON(t, w, map[string]any{
				"errcode": 0,
				"errmsg":  "ok",
				"group_chat": map[string]any{
					"chat_id": "room-customer",
					"name":    "客户群A",
					"owner":   "lisi",
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	customerGroup, err := clawman.ResolveGroup(ctx, "room-customer", nil)
	if err != nil {
		t.Fatalf("ResolveGroup customer: %v", err)
	}
	internalGroup, err := clawman.ResolveGroup(ctx, "room-internal", nil)
	if err != nil {
		t.Fatalf("ResolveGroup internal: %v", err)
	}
	internalGroupCached, err := clawman.ResolveGroup(ctx, "room-internal", nil)
	if err != nil {
		t.Fatalf("ResolveGroup internal cached: %v", err)
	}

	if customerGroup.Type != "customer_group" || customerGroup.Name != "客户群A" {
		t.Fatalf("customerGroup = %+v", customerGroup)
	}
	if internalGroup.Type != "internal_group" || internalGroup.Name != "内部群B" {
		t.Fatalf("internalGroup = %+v", internalGroup)
	}
	if internalGroupCached.Type != "internal_group" || internalGroupCached.Name != "内部群B" {
		t.Fatalf("internalGroupCached = %+v", internalGroupCached)
	}
	if archiveGroupCalls != 2 {
		t.Fatalf("archive group API calls = %d, want 2", archiveGroupCalls)
	}
	if customerGroupCalls != 1 {
		t.Fatalf("customer group API calls = %d, want 1", customerGroupCalls)
	}
}

func TestResolveGroupFailureDoesNotCachePlaceholder(t *testing.T) {
	var archiveGroupCalls int
	var customerGroupCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/msgaudit/groupchat/get":
			archiveGroupCalls++
			writeJSON(t, w, map[string]any{
				"errcode": 40001,
				"errmsg":  "archive miss",
			})
		case "/cgi-bin/externalcontact/groupchat/get":
			customerGroupCalls++
			writeJSON(t, w, map[string]any{
				"errcode": 40002,
				"errmsg":  "customer miss",
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	if _, err := clawman.ResolveGroup(ctx, "room-missing", nil); err == nil {
		t.Fatal("ResolveGroup error = nil, want non-nil")
	}
	if _, err := clawman.ResolveGroup(ctx, "room-missing", nil); err == nil {
		t.Fatal("ResolveGroup second error = nil, want non-nil")
	}
	if archiveGroupCalls != 2 {
		t.Fatalf("archive group API calls = %d, want 2", archiveGroupCalls)
	}
	if customerGroupCalls != 2 {
		t.Fatalf("customer group API calls = %d, want 2", customerGroupCalls)
	}
}

func TestResolveGroupUsesCustomerAPIForExternalSender(t *testing.T) {
	var customerGroupCalls int
	var archiveGroupCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/msgaudit/groupchat/get":
			archiveGroupCalls++
			writeJSON(t, w, map[string]any{"errcode": 0, "errmsg": "ok", "roomname": "不该调用"})
		case "/cgi-bin/externalcontact/groupchat/get":
			customerGroupCalls++
			writeJSON(t, w, map[string]any{
				"errcode": 0,
				"errmsg":  "ok",
				"group_chat": map[string]any{
					"chat_id": "room-customer",
					"name":    "客户群A",
					"owner":   "lisi",
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	group, err := clawman.ResolveGroup(ctx, "room-customer", &Identity{UserID: "wm123", Type: "external", Name: "外部联系人A"})
	if err != nil {
		t.Fatalf("ResolveGroup external sender: %v", err)
	}

	if group.Type != "customer_group" || group.Name != "客户群A" {
		t.Fatalf("group = %+v", group)
	}
	if customerGroupCalls != 1 {
		t.Fatalf("customer group API calls = %d, want 1", customerGroupCalls)
	}
	if archiveGroupCalls != 0 {
		t.Fatalf("archive group API calls = %d, want 0", archiveGroupCalls)
	}
}

func TestResolveGroupUsesArchiveAPIForInternalSender(t *testing.T) {
	var customerGroupCalls int
	var archiveGroupCalls int

	server := newWeComTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/msgaudit/groupchat/get":
			archiveGroupCalls++
			writeJSON(t, w, map[string]any{
				"errcode":  0,
				"errmsg":   "ok",
				"roomname": "内部群B",
			})
		case "/cgi-bin/externalcontact/groupchat/get":
			customerGroupCalls++
			writeJSON(t, w, map[string]any{"errcode": 0, "errmsg": "ok"})
		default:
			http.NotFound(w, r)
		}
	})

	clawman := newTestClawman(t, server.URL)
	ctx := context.Background()

	group, err := clawman.ResolveGroup(ctx, "room-internal", &Identity{UserID: "zhangsan", Type: "employee", Name: "张三"})
	if err != nil {
		t.Fatalf("ResolveGroup internal sender: %v", err)
	}

	if group.Type != "internal_group" || group.Name != "内部群B" {
		t.Fatalf("group = %+v", group)
	}
	if archiveGroupCalls != 1 {
		t.Fatalf("archive group API calls = %d, want 1", archiveGroupCalls)
	}
	if customerGroupCalls != 0 {
		t.Fatalf("customer group API calls = %d, want 0", customerGroupCalls)
	}
}
