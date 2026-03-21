package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdksandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

type fakeSDKHandle struct {
	openCalls  int
	closeCalls int
	runCalls   []string
	ready      bool
	sandboxID  string
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
	handle := &fakeSDKHandle{sandboxID: "sandbox-123"}
	var factoryCalls int

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		factoryCalls++
		if opts.TemplateName != "tinyclaw-agent-template" {
			t.Fatalf("template = %q, want %q", opts.TemplateName, "tinyclaw-agent-template")
		}
		if opts.Namespace != "claw" {
			t.Fatalf("namespace = %q, want %q", opts.Namespace, "claw")
		}
		if opts.APIURL != "http://sandbox-router-svc.claw.svc.cluster.local:8080" {
			t.Fatalf("api url = %q", opts.APIURL)
		}
		return handle, nil
	}

	handle.runResult = &sdksandbox.ExecutionResult{
		Stdout: `{"stdout":"agent reply","stderr":"","exit_code":0}`,
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
	orphanedHandle := &fakeSDKHandle{
		openErr:  sdksandbox.ErrOrphanedClaim,
		closeErr: nil,
	}
	replacementHandle := &fakeSDKHandle{
		runResult: &sdksandbox.ExecutionResult{
			Stdout: `{"stdout":"agent reply","stderr":"","exit_code":0}`,
		},
	}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})

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

func TestInvokeAgentRunsEncodedAgentRequest(t *testing.T) {
	handle := &fakeSDKHandle{
		ready:     true,
		sandboxID: "sandbox-123",
		runResult: &sdksandbox.ExecutionResult{
			Stdout: `{"stdout":"agent reply","stderr":"","exit_code":0}`,
		},
	}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
		ServerPort:   8888,
	})
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
	if len(handle.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1", len(handle.runCalls))
	}
	command := handle.runCalls[0]
	if !strings.Contains(command, "http://127.0.0.1:8888/agent") {
		t.Fatalf("command missing local agent endpoint: %s", command)
	}
	if !strings.Contains(command, "base64 -d") {
		t.Fatalf("command missing base64 decode: %s", command)
	}
}

func TestInvokeAgentPropagatesRunError(t *testing.T) {
	wantErr := errors.New("run failed")
	handle := &fakeSDKHandle{
		ready:     true,
		runErr:    wantErr,
		sandboxID: "sandbox-123",
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
}

func TestInvokeAgentReopensAfterRunNotReady(t *testing.T) {
	handle := &fakeSDKHandle{
		ready:   true,
		runErrs: []error{sdksandbox.ErrNotReady},
		runResult: &sdksandbox.ExecutionResult{
			Stdout: `{"stdout":"agent reply","stderr":"","exit_code":0}`,
		},
	}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		return handle, nil
	}

	resp, err := orch.InvokeAgent(context.Background(), "room-1", testAgentRequest("hello"))
	if err != nil {
		t.Fatalf("InvokeAgent error: %v", err)
	}
	if resp.Stdout != "agent reply" {
		t.Fatalf("stdout = %q, want %q", resp.Stdout, "agent reply")
	}
	if handle.openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", handle.openCalls)
	}
	if len(handle.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(handle.runCalls))
	}
}

func TestCloseClosesAllSDKClients(t *testing.T) {
	handle1 := &fakeSDKHandle{ready: true}
	handle2 := &fakeSDKHandle{ready: true}

	orch := NewOrchestrator(Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		APIURL:       "http://sandbox-router-svc.claw.svc.cluster.local:8080",
	})
	handles := []sdkHandle{handle1, handle2}
	var next int
	orch.factory = func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
		h := handles[next]
		next++
		return h, nil
	}

	handle1.runResult = &sdksandbox.ExecutionResult{
		Stdout: `{"stdout":"reply-1","stderr":"","exit_code":0}`,
	}
	handle2.runResult = &sdksandbox.ExecutionResult{
		Stdout: `{"stdout":"reply-2","stderr":"","exit_code":0}`,
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
