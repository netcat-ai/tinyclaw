package executor

import (
	"context"
	"fmt"
	"testing"

	"tinyclaw/internal/core"
)

type fakeStore struct {
	started   []int64
	completed []int64
	failed    []int64
}

func (s *fakeStore) StartCoreInvocation(_ context.Context, invocationID int64) (core.Invocation, error) {
	s.started = append(s.started, invocationID)
	return core.Invocation{ID: invocationID, RoomID: 10, Status: core.InvocationStatusRunning}, nil
}

func (s *fakeStore) CompleteCoreInvocation(_ context.Context, invocationID int64, _ core.CompleteInvocationInput) (core.InvocationResult, error) {
	s.completed = append(s.completed, invocationID)
	return core.InvocationResult{Invocation: core.Invocation{ID: invocationID, Status: core.InvocationStatusCompleted}}, nil
}

func (s *fakeStore) FailCoreInvocation(_ context.Context, invocationID int64, _ string) (core.InvocationResult, error) {
	s.failed = append(s.failed, invocationID)
	return core.InvocationResult{Invocation: core.Invocation{ID: invocationID, Status: core.InvocationStatusFailed}}, nil
}

type errorRunner struct{}

func (errorRunner) RunInvocation(context.Context, core.Invocation) (core.CompleteInvocationInput, error) {
	return core.CompleteInvocationInput{}, fmt.Errorf("boom")
}

func TestRunOnceCompletesInvocation(t *testing.T) {
	store := &fakeStore{}
	scheduler := NewScheduler(context.Background(), store, StaticRunner{Text: "done"})

	scheduler.RunOnce(context.Background(), 1000)

	if len(store.started) != 1 || store.started[0] != 1000 {
		t.Fatalf("started = %#v, want [1000]", store.started)
	}
	if len(store.completed) != 1 || store.completed[0] != 1000 {
		t.Fatalf("completed = %#v, want [1000]", store.completed)
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
	if len(store.completed) != 0 {
		t.Fatalf("completed = %#v, want empty", store.completed)
	}
}
