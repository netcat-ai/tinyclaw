package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

type fakeStore struct {
	claimed         bool
	completed       bool
	failed          bool
	contextMessages []core.Message
	completedResult core.AgentRunResult
	failedDetail    string
}

func (s *fakeStore) ClaimNextAgentRun(context.Context, string, time.Duration) (core.AgentRun, bool, error) {
	if s.claimed {
		return core.AgentRun{}, false, nil
	}
	s.claimed = true
	return core.AgentRun{
		AgentSessionID:      100,
		RoomID:              10,
		SourceMessageFromID: 20,
		SourceMessageToID:   22,
		LockOwner:           "test",
	}, true, nil
}

func (s *fakeStore) ListAgentRunMessages(context.Context, core.AgentRun) ([]core.Message, error) {
	return s.contextMessages, nil
}

func (s *fakeStore) CompleteAgentRun(_ context.Context, _ core.AgentRun, result core.AgentRunResult) (*core.Delivery, error) {
	s.completed = true
	s.completedResult = result
	return nil, nil
}

func (s *fakeStore) FailAgentRun(_ context.Context, _ core.AgentRun, detail string) (*core.Delivery, error) {
	s.failed = true
	s.failedDetail = detail
	return nil, nil
}

type errorRunner struct{}

func (errorRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, fmt.Errorf("boom")
}

type contextRunner struct {
	contextCount int
}

func (r *contextRunner) RunAgent(_ context.Context, run AgentRunRequest) (core.AgentRunResult, error) {
	r.contextCount = len(run.ContextMessages)
	return core.AgentRunResult{
		FinalOutput: "done",
		MemoryWriteProposals: []core.MemoryWriteProposal{{
			Op:      core.MemoryWriteOpUpsertFact,
			Key:     "project",
			Content: "TinyClaw has Room Memory.",
		}},
	}, nil
}

func TestRunOnceCompletesAgentRun(t *testing.T) {
	store := &fakeStore{
		contextMessages: []core.Message{{ID: 21}, {ID: 22}},
	}
	runner := &contextRunner{}
	scheduler := NewScheduler(context.Background(), store, runner)

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}

	if !store.completed {
		t.Fatal("completed = false, want true")
	}
	if store.completedResult.FinalOutput != "done" {
		t.Fatalf("completed text = %q, want done", store.completedResult.FinalOutput)
	}
	if len(store.completedResult.MemoryWriteProposals) != 1 {
		t.Fatalf("memory proposals = %d, want 1", len(store.completedResult.MemoryWriteProposals))
	}
	if runner.contextCount != 2 {
		t.Fatalf("context count = %d, want 2", runner.contextCount)
	}
	if store.failed {
		t.Fatal("failed = true, want false")
	}
}

func TestRunOnceFailsAgentRunOnRunnerError(t *testing.T) {
	store := &fakeStore{}
	scheduler := NewScheduler(context.Background(), store, errorRunner{})

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}

	if !store.failed {
		t.Fatal("failed = false, want true")
	}
	if store.failedDetail != "boom" {
		t.Fatalf("failed detail = %q, want boom", store.failedDetail)
	}
	if store.completed {
		t.Fatal("completed = true, want false")
	}
}
