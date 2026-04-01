package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdksandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

type fakeSDKHandle struct {
	openCalls  int
	closeCalls int
	runCalls   []string
	ready      bool
	claimName  string
	openErr    error
	openErrs   []error
	runErrs    []error
	runResult  *sdksandbox.ExecutionResult
	runErr     error
	closeErr   error
}

func (f *fakeSDKHandle) Open(ctx context.Context) error {
	f.openCalls++
	if len(f.openErrs) > 0 {
		err := f.openErrs[0]
		f.openErrs = f.openErrs[1:]
		if err != nil {
			return err
		}
		f.ready = true
		return nil
	}
	if f.openErr != nil {
		return f.openErr
	}
	f.ready = true
	return nil
}

func (f *fakeSDKHandle) Close(ctx context.Context) error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeSDKHandle) IsReady() bool {
	return f.ready
}

func (f *fakeSDKHandle) ClaimName() string {
	return f.claimName
}

func (f *fakeSDKHandle) Run(ctx context.Context, command string, opts ...sdksandbox.CallOption) (*sdksandbox.ExecutionResult, error) {
	f.runCalls = append(f.runCalls, command)
	if len(f.runErrs) > 0 {
		err := f.runErrs[0]
		f.runErrs = f.runErrs[1:]
		if err != nil {
			if errors.Is(err, sdksandbox.ErrNotReady) {
				f.ready = false
			}
			return nil, err
		}
	}
	if f.runErr != nil {
		if errors.Is(f.runErr, sdksandbox.ErrNotReady) {
			f.ready = false
		}
		return nil, f.runErr
	}
	if f.runResult != nil {
		return f.runResult, nil
	}
	return &sdksandbox.ExecutionResult{}, nil
}

func testAgentRequest(msg string) AgentRequest {
	return AgentRequest{
		MsgID:    "msg-test",
		RoomID:   "room-1",
		TenantID: "tenant-test",
		ChatType: "group",
		Messages: []AgentMessage{
			{
				Seq:     1,
				MsgID:   "msg-test",
				FromID:  "user-test",
				Payload: `{"msgtype":"text","text":{"content":"` + msg + `"}}`,
			},
		},
	}
}

func TestInvokeAgentCreatesAndReusesRoomSession(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/agent" {
			t.Fatalf("path = %q, want /agent", r.URL.Path)
		}
		if got := r.Header.Get("X-Sandbox-ID"); got != "sandbox-123" {
			t.Fatalf("X-Sandbox-ID = %q, want sandbox-123", got)
		}
		_, _ = w.Write([]byte(`{"stdout":"agent reply","stderr":"","exit_code":0}`))
	}))
	defer server.Close()

	handle := &fakeSDKHandle{claimName: "sandbox-123"}
	var factoryCalls int

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
	})
	orch.http = server.Client()
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		factoryCalls++
		if opts.TemplateName != "tinyclaw-agent-template" {
			t.Fatalf("template = %q, want %q", opts.TemplateName, "tinyclaw-agent-template")
		}
		if opts.Namespace != "claw" {
			t.Fatalf("namespace = %q, want %q", opts.Namespace, "claw")
		}
		if opts.APIURL != server.URL {
			t.Fatalf("api url = %q", opts.APIURL)
		}
		return handle, nil
	}

	if _, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello")); err != nil {
		t.Fatalf("InvokeAgent first call error: %v", err)
	}
	if _, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello again")); err != nil {
		t.Fatalf("InvokeAgent second call error: %v", err)
	}

	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	if handle.openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", handle.openCalls)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestInvokeAgentPropagatesOpenError(t *testing.T) {
	wantErr := errors.New("boom")
	handle := &fakeSDKHandle{openErr: wantErr}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	_, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("InvokeAgent error = %v, want wrapped %v", err, wantErr)
	}
}

func TestInvokeAgentRecreatesRoomSessionAfterOrphanedClaim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Sandbox-ID"); got != "sandbox-456" {
			t.Fatalf("X-Sandbox-ID = %q, want sandbox-456", got)
		}
		_, _ = w.Write([]byte(`{"stdout":"agent reply","stderr":"","exit_code":0}`))
	}))
	defer server.Close()

	orphanedHandle := &fakeSDKHandle{
		openErr:  sdksandbox.ErrOrphanedClaim,
		closeErr: nil,
	}
	replacementHandle := &fakeSDKHandle{
		claimName: "sandbox-456",
	}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
	})
	orch.http = server.Client()

	handles := []sdkHandle{orphanedHandle, replacementHandle}
	var factoryCalls int
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		h := handles[factoryCalls]
		factoryCalls++
		return h, nil
	}

	resp, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if err != nil {
		t.Fatalf("InvokeAgent error: %v", err)
	}
	if resp.Stdout != "agent reply" {
		t.Fatalf("stdout = %q, want %q", resp.Stdout, "agent reply")
	}
	if factoryCalls != 2 {
		t.Fatalf("factory calls = %d, want 2", factoryCalls)
	}
	if orphanedHandle.closeCalls != 1 {
		t.Fatalf("orphaned handle close calls = %d, want 1", orphanedHandle.closeCalls)
	}
	if replacementHandle.openCalls != 1 {
		t.Fatalf("replacement handle open calls = %d, want 1", replacementHandle.openCalls)
	}
}

func TestInvokeAgentReturnsCleanupErrorWhenOrphanedClaimCloseFails(t *testing.T) {
	wantErr := errors.New("delete failed")
	handle := &fakeSDKHandle{
		openErr:  sdksandbox.ErrOrphanedClaim,
		closeErr: wantErr,
	}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	_, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("InvokeAgent error = %v, want wrapped %v", err, wantErr)
	}
	if handle.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", handle.closeCalls)
	}
}

func TestInvokeAgentPostsStructuredRequest(t *testing.T) {
	var gotRequest AgentRequest
	handle := &fakeSDKHandle{
		ready:     true,
		claimName: "sandbox-123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/agent" {
			t.Fatalf("path = %q, want /agent", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if got := r.Header.Get("X-Sandbox-ID"); got != "sandbox-123" {
			t.Fatalf("X-Sandbox-ID = %q, want sandbox-123", got)
		}
		if got := r.Header.Get("X-Sandbox-Namespace"); got != "claw" {
			t.Fatalf("X-Sandbox-Namespace = %q, want claw", got)
		}
		if got := r.Header.Get("X-Sandbox-Port"); got != "8888" {
			t.Fatalf("X-Sandbox-Port = %q, want 8888", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"stdout":"agent reply","stderr":"","exit_code":0}`))
	}))
	defer server.Close()

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
		ServerPort:   8888,
	})
	orch.http = server.Client()
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	resp, err := orch.InvokeAgent(context.Background(), "room-1", AgentRequest{
		MsgID:    "msg-1",
		RoomID:   "room-1",
		TenantID: "corp-id",
		ChatType: "group",
		Messages: []AgentMessage{
			{
				Seq:      1,
				MsgID:    "msg-1",
				FromID:   "zhangsan",
				FromName: "张三",
				MsgTime:  "2026-03-21T10:00:00Z",
				Payload:  `{"msgtype":"text","text":{"content":"hello"}}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("InvokeAgent error: %v", err)
	}
	if resp.Stdout != "agent reply" {
		t.Fatalf("stdout = %q, want %q", resp.Stdout, "agent reply")
	}
	if gotRequest.RoomID != "room-1" || gotRequest.TenantID != "corp-id" || gotRequest.ChatType != "group" {
		t.Fatalf("unexpected request envelope: %+v", gotRequest)
	}
	if len(gotRequest.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(gotRequest.Messages))
	}
	if gotRequest.Messages[0].Payload != `{"msgtype":"text","text":{"content":"hello"}}` {
		t.Fatalf("payload = %q", gotRequest.Messages[0].Payload)
	}
}

func TestInvokeAgentPropagatesHTTPError(t *testing.T) {
	handle := &fakeSDKHandle{
		ready:     true,
		claimName: "sandbox-123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"router temporarily unavailable"}`))
	}))
	defer server.Close()

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
	})
	orch.http = server.Client()
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	_, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if err == nil || !strings.Contains(err.Error(), "router temporarily unavailable") {
		t.Fatalf("InvokeAgent error = %v, want router error", err)
	}
}

func TestInvokeAgentReturnsRuntimeFailure(t *testing.T) {
	handle := &fakeSDKHandle{ready: true, claimName: "sandbox-123"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"stdout":"","stderr":"tool failed","exit_code":1}`))
	}))
	defer server.Close()

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
	})
	orch.http = server.Client()
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	_, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if err == nil || !strings.Contains(err.Error(), "tool failed") {
		t.Fatalf("InvokeAgent error = %v, want runtime failure", err)
	}
}

func TestCloseClosesAllSDKClients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := "reply-1"
		if r.Header.Get("X-Sandbox-ID") == "sandbox-2" {
			reply = "reply-2"
		}
		_, _ = w.Write([]byte(`{"stdout":"` + reply + `","stderr":"","exit_code":0}`))
	}))
	defer server.Close()

	handle1 := &fakeSDKHandle{ready: true, claimName: "sandbox-1"}
	handle2 := &fakeSDKHandle{ready: true, claimName: "sandbox-2"}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       server.URL,
	})
	orch.http = server.Client()
	handles := []sdkHandle{handle1, handle2}
	var next int
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		h := handles[next]
		next++
		return h, nil
	}

	if _, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello-1")); err != nil {
		t.Fatalf("InvokeAgent room-1: %v", err)
	}
	if _, err := orch.InvokeAgent(context.Background(), "room-2", testAgentRequest("hello-2")); err != nil {
		t.Fatalf("InvokeAgent room-2: %v", err)
	}

	if err := orch.Close(context.Background()); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if handle1.closeCalls != 1 || handle2.closeCalls != 1 {
		t.Fatalf("close calls = %d,%d want 1,1", handle1.closeCalls, handle2.closeCalls)
	}
}
