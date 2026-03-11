package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"tinyclaw/wecom"
)

func newTestClawman(t *testing.T, serverURL string) (*Clawman, *redis.Client) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		rdb.Close()
		mr.Close()
	})

	wecom.SetBaseURL(serverURL)
	t.Cleanup(wecom.ResetBaseURL)

	return &Clawman{
		redis:      rdb,
		contactAPI: wecom.NewClient("corp-id", "contact-secret"),
		archiveAPI: wecom.NewClient("corp-id", "corp-secret"),
	}, rdb
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

func TestResolveExternalContactUsesCustomerAPIAndCachesOneHour(t *testing.T) {
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

	clawman, rdb := newTestClawman(t, server.URL)
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

	ttl := rdb.TTL(ctx, externalContactCachePrefix+"wm123").Val()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Fatalf("cache ttl = %s, want about 1h", ttl)
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

	clawman, _ := newTestClawman(t, server.URL)
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

	clawman, rdb := newTestClawman(t, server.URL)
	ctx := context.Background()

	customerGroup, err := clawman.ResolveGroup(ctx, "room-customer")
	if err != nil {
		t.Fatalf("ResolveGroup customer: %v", err)
	}
	internalGroup, err := clawman.ResolveGroup(ctx, "room-internal")
	if err != nil {
		t.Fatalf("ResolveGroup internal: %v", err)
	}
	internalGroupCached, err := clawman.ResolveGroup(ctx, "room-internal")
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

	ttl := rdb.TTL(ctx, groupDetailCachePrefix+"room-internal").Val()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Fatalf("group cache ttl = %s, want about 1h", ttl)
	}
}
