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
	listErr         error
	contextMessages []core.Message
	agents          []core.Agent
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
	if s.listErr != nil {
		return nil, s.listErr
	}
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

func (s *fakeStore) ListAgents(context.Context) ([]core.Agent, error) {
	return s.agents, nil
}

type errorRunner struct{}

func (errorRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, fmt.Errorf("boom")
}

type contextRunner struct {
	contextCount int
	text         string
	lastRun      AgentRunRequest
}

func (r *contextRunner) RunAgent(_ context.Context, run AgentRunRequest) (core.AgentRunResult, error) {
	r.contextCount = len(run.ContextMessages)
	r.lastRun = run
	text := r.text
	if text == "" {
		text = "done"
	}
	return core.AgentRunResult{
		FinalOutput: text,
		MemoryWriteProposals: []core.MemoryWriteProposal{{
			Op:      core.MemoryWriteOpUpsertFact,
			Key:     "project",
			Content: "TinyClaw has Room Memory.",
		}},
	}, nil
}

type emptyOutputRunner struct{}

func (emptyOutputRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, nil
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

func TestRunOnceFailsAgentRunWhenContextMessagesCannotBeLoaded(t *testing.T) {
	store := &fakeStore{listErr: fmt.Errorf("list failed")}
	scheduler := NewScheduler(context.Background(), store, &contextRunner{})

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}
	if !store.failed {
		t.Fatal("failed = false, want true")
	}
	if store.failedDetail != "list failed" {
		t.Fatalf("failed detail = %q, want list failed", store.failedDetail)
	}
	if store.completed {
		t.Fatal("completed = true, want false")
	}
}

func TestRunOnceCompletesAgentRunWithEmptyOutput(t *testing.T) {
	store := &fakeStore{}
	scheduler := NewScheduler(context.Background(), store, emptyOutputRunner{})

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}
	if !store.completed {
		t.Fatal("completed = false, want true")
	}
	if store.completedResult.FinalOutput != "" {
		t.Fatalf("final output = %q, want empty", store.completedResult.FinalOutput)
	}
	if store.failed {
		t.Fatal("failed = true, want false")
	}
}

func TestRunOnceSelectsMentionedAgents(t *testing.T) {
	store := &fakeStore{
		contextMessages: []core.Message{
			{ID: 1, Payload: []byte(`{"text":"@product @测试 帮忙看下"}`)},
		},
		agents: []core.Agent{
			{ID: 1, Key: "product", DisplayName: "Product", Prompt: "Product prompt.", Enabled: true},
			{ID: 2, Key: "qa", DisplayName: "测试", Prompt: "QA prompt.", Enabled: true},
			{ID: 3, Key: "ops", DisplayName: "Ops", Prompt: "Ops prompt.", Enabled: false},
		},
	}
	runner := &contextRunner{}
	scheduler := NewScheduler(context.Background(), store, runner)

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}
	if len(runner.lastRun.SelectedAgents) != 2 {
		t.Fatalf("selected agents = %+v, want product and qa", runner.lastRun.SelectedAgents)
	}
	if runner.lastRun.SelectedAgents[0].Key != "product" || runner.lastRun.SelectedAgents[1].Key != "qa" {
		t.Fatalf("selected agents = %+v, want product and qa", runner.lastRun.SelectedAgents)
	}
}
