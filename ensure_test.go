package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeEnsureLocker struct {
	acquired bool
	err      error
	key      string
	ttl      time.Duration
	calls    int
}

func (l *fakeEnsureLocker) Acquire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	l.calls++
	l.key = key
	l.ttl = ttl
	return l.acquired, l.err
}

type fakeEnsureHTTPClient struct {
	resp *http.Response
	err  error

	calls   int
	method  string
	url     string
	payload string
	headers http.Header
}

func (c *fakeEnsureHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	c.method = req.Method
	c.url = req.URL.String()
	c.headers = req.Header.Clone()
	body, _ := io.ReadAll(req.Body)
	c.payload = string(body)

	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestEnsureLockKey(t *testing.T) {
	if got := ensureLockKey("lock:ensure", "session-1"); got != "lock:ensure:session-1" {
		t.Fatalf("ensureLockKey() = %q, want %q", got, "lock:ensure:session-1")
	}
}

func TestNewSessionRuntimeEnsurerFallsBackToNoopWhenEnsureURLMissing(t *testing.T) {
	ensurer := newSessionRuntimeEnsurer(Config{}, nil)
	if _, ok := ensurer.(noopSessionRuntimeEnsurer); !ok {
		t.Fatalf("newSessionRuntimeEnsurer() type = %T, want noopSessionRuntimeEnsurer", ensurer)
	}
}

func TestHTTPEnsurerSkipsWhenLockNotAcquired(t *testing.T) {
	locker := &fakeEnsureLocker{acquired: false}
	httpClient := &fakeEnsureHTTPClient{}
	ensurer := &httpSessionRuntimeEnsurer{
		url:        "http://runtime/internal/session-runtime/ensure",
		lockPrefix: "lock:ensure",
		lockTTL:    3 * time.Second,
		locker:     locker,
		httpClient: httpClient,
	}

	err := ensurer.Ensure(context.Background(), IngressEvent{SessionKey: "s1"})
	if err != nil {
		t.Fatalf("Ensure() error = %v, want nil", err)
	}
	if locker.calls != 1 {
		t.Fatalf("Acquire calls = %d, want 1", locker.calls)
	}
	if httpClient.calls != 0 {
		t.Fatalf("http calls = %d, want 0", httpClient.calls)
	}
}

func TestHTTPEnsurerPostsExpectedPayload(t *testing.T) {
	locker := &fakeEnsureLocker{acquired: true}
	httpClient := &fakeEnsureHTTPClient{
		resp: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"runtime_state":"starting"}`))},
	}
	ensurer := &httpSessionRuntimeEnsurer{
		url:        "http://runtime/internal/session-runtime/ensure",
		lockPrefix: "lock:ensure",
		lockTTL:    3 * time.Second,
		locker:     locker,
		httpClient: httpClient,
	}
	event := IngressEvent{
		SessionKey: "session-a",
		TenantID:   "tenant-1",
		ChatType:   chatTypeGroup,
		TraceID:    "trace-1",
	}

	err := ensurer.Ensure(context.Background(), event)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if locker.key != "lock:ensure:session-a" {
		t.Fatalf("Acquire key = %q, want %q", locker.key, "lock:ensure:session-a")
	}
	if locker.ttl != 3*time.Second {
		t.Fatalf("Acquire ttl = %v, want 3s", locker.ttl)
	}
	if httpClient.calls != 1 {
		t.Fatalf("http calls = %d, want 1", httpClient.calls)
	}
	if httpClient.method != http.MethodPost {
		t.Fatalf("method = %q, want %q", httpClient.method, http.MethodPost)
	}
	if got := httpClient.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	wantPayload := `{"session_key":"session-a","tenant_id":"tenant-1","chat_type":"group","trace_id":"trace-1"}`
	if httpClient.payload != wantPayload {
		t.Fatalf("payload = %q, want %q", httpClient.payload, wantPayload)
	}
}

func TestHTTPEnsurerReturnsErrorOnNon2xx(t *testing.T) {
	locker := &fakeEnsureLocker{acquired: true}
	httpClient := &fakeEnsureHTTPClient{
		resp: &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("boom"))},
	}
	ensurer := &httpSessionRuntimeEnsurer{
		url:        "http://runtime/internal/session-runtime/ensure",
		lockPrefix: "lock:ensure",
		lockTTL:    3 * time.Second,
		locker:     locker,
		httpClient: httpClient,
	}

	err := ensurer.Ensure(context.Background(), IngressEvent{SessionKey: "session-a"})
	if err == nil {
		t.Fatal("Ensure() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "status=500") {
		t.Fatalf("Ensure() error = %q, want contains %q", err.Error(), "status=500")
	}
}
