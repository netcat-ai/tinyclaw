package executor

import (
	"context"
	"fmt"
	"testing"

	"tinyclaw/internal/core"
)

type fakeStore struct {
	started         []int64
	completed       []int64
	failed          []int64
	contextMessages []core.Message
	newMessages     []core.Message
	completedText   string
	failedDetail    string
}

func (s *fakeStore) StartCoreInvocation(_ context.Context, invocationID int64) (core.Invocation, error) {
	s.started = append(s.started, invocationID)
	return core.Invocation{ID: invocationID, RoomID: 10, Status: core.InvocationStatusRunning}, nil
}

func (s *fakeStore) ListCoreInvocationContextMessages(_ context.Context, invocationID int64) ([]core.Message, error) {
	if invocationID != 1000 && invocationID != 1001 {
		return nil, fmt.Errorf("unexpected context invocation id %d", invocationID)
	}
	return s.contextMessages, nil
}

func (s *fakeStore) ReadCoreInvocationNewMessages(_ context.Context, invocationID int64) ([]core.Message, error) {
	if invocationID != 1000 && invocationID != 1001 {
		return nil, fmt.Errorf("unexpected read invocation id %d", invocationID)
	}
	return s.newMessages, nil
}

func (s *fakeStore) CompleteCoreInvocation(_ context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error) {
	s.completed = append(s.completed, invocationID)
	s.completedText = input.Text
	return core.InvocationResult{Invocation: core.Invocation{ID: invocationID, Status: core.InvocationStatusCompleted}}, nil
}

func (s *fakeStore) FailCoreInvocation(_ context.Context, invocationID int64, detail string) (core.InvocationResult, error) {
	s.failed = append(s.failed, invocationID)
	s.failedDetail = detail
	return core.InvocationResult{Invocation: core.Invocation{ID: invocationID, Status: core.InvocationStatusFailed}}, nil
}

type errorRunner struct{}

func (errorRunner) RunInvocation(context.Context, InvocationRun) (string, error) {
	return "", fmt.Errorf("boom")
}

type contextRunner struct {
	contextCount int
	newCount     int
}

func (r *contextRunner) RunInvocation(ctx context.Context, run InvocationRun) (string, error) {
	r.contextCount = len(run.ContextMessages)
	messages, err := run.ReadNewMessages(ctx)
	if err != nil {
		return "", err
	}
	r.newCount = len(messages)
	return "done", nil
}

func TestRunOnceCompletesInvocation(t *testing.T) {
	store := &fakeStore{
		contextMessages: []core.Message{{ID: 20}, {ID: 21}},
		newMessages:     []core.Message{{ID: 22}},
	}
	runner := &contextRunner{}
	scheduler := NewScheduler(context.Background(), store, runner)

	scheduler.RunOnce(context.Background(), 1000)

	if len(store.started) != 1 || store.started[0] != 1000 {
		t.Fatalf("started = %#v, want [1000]", store.started)
	}
	if len(store.completed) != 1 || store.completed[0] != 1000 {
		t.Fatalf("completed = %#v, want [1000]", store.completed)
	}
	if store.completedText != "done" {
		t.Fatalf("completed text = %q, want done", store.completedText)
	}
	if runner.contextCount != 2 {
		t.Fatalf("context count = %d, want 2", runner.contextCount)
	}
	if runner.newCount != 1 {
		t.Fatalf("new count = %d, want 1", runner.newCount)
	}
	if len(store.failed) != 0 {
		t.Fatalf("failed = %#v, want empty", store.failed)
	}
}

func TestRunOnceFailsInvocationOnRunnerError(t *testing.T) {
	store := &fakeStore{}
	scheduler := NewScheduler(context.Background(), store, errorRunner{})

	scheduler.RunOnce(context.Background(), 1001)

	if len(store.started) != 1 || store.started[0] != 1001 {
		t.Fatalf("started = %#v, want [1001]", store.started)
	}
	if len(store.failed) != 1 || store.failed[0] != 1001 {
		t.Fatalf("failed = %#v, want [1001]", store.failed)
	}
	if store.failedDetail != "boom" {
		t.Fatalf("failed detail = %q, want boom", store.failedDetail)
	}
	if len(store.completed) != 0 {
		t.Fatalf("completed = %#v, want empty", store.completed)
	}
}
