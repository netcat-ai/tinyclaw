package executor

import (
	"context"
	"fmt"
	"sync"
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
	mediaDeliveries chan core.GeneratedMediaOutput
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

func (s *fakeStore) CreateGeneratedMediaDelivery(_ context.Context, _ core.AgentRun, media core.GeneratedMediaOutput) (*core.Delivery, error) {
	if s.mediaDeliveries != nil {
		s.mediaDeliveries <- media
	}
	return &core.Delivery{ID: 200}, nil
}

func (s *fakeStore) ListAgents(context.Context) ([]core.Agent, error) {
	return s.agents, nil
}

type errorRunner struct{}

func (errorRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, fmt.Errorf("boom")
}

type contextRunner struct {
	contextCount    int
	text            string
	backgroundTasks []core.BackgroundCodexTask
	lastRun         AgentRunRequest
}

func (r *contextRunner) RunAgent(_ context.Context, run AgentRunRequest) (core.AgentRunResult, error) {
	r.contextCount = len(run.ContextMessages)
	r.lastRun = run
	text := r.text
	if text == "" {
		text = "done"
	}
	return core.AgentRunResult{
		FinalOutput:          text,
		BackgroundCodexTasks: r.backgroundTasks,
		MemoryWriteProposals: []core.MemoryWriteProposal{{
			Op:      core.MemoryWriteOpUpsertFact,
			Key:     "project",
			Content: "TinyClaw has Room Memory.",
		}},
	}, nil
}

type schedulerBackgroundTaskRunner struct {
	tasks   chan core.BackgroundCodexTask
	release chan struct{}
}

func (r schedulerBackgroundTaskRunner) RunBackgroundCodexTask(_ context.Context, _ AgentRunRequest, task core.BackgroundCodexTask) (core.BackgroundCodexTaskResult, error) {
	r.tasks <- task
	<-r.release
	return core.BackgroundCodexTaskResult{
		Artifacts: []core.BackgroundArtifact{{
			Path:     "/tmp/generated.jpg",
			MIMEType: "image/jpeg",
		}},
	}, nil
}

type schedulerArtifactStore struct{}

func (s schedulerArtifactStore) StoreBackgroundArtifact(context.Context, core.BackgroundArtifact) (core.GeneratedMediaOutput, error) {
	return core.GeneratedMediaOutput{
		MediaID:      "gm_async",
		MediaURL:     "https://media.example/gm_async.png",
		MediaURLKind: "presigned_s3",
		MIMEType:     "image/png",
	}, nil
}

type emptyOutputRunner struct{}

func (emptyOutputRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, nil
}

type backgroundTaskRunner struct {
	tasks []core.BackgroundCodexTask
}

func (r backgroundTaskRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{BackgroundCodexTasks: r.tasks}, nil
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

func TestRunOnceStartsBackgroundCodexTaskAsync(t *testing.T) {
	store := &fakeStore{mediaDeliveries: make(chan core.GeneratedMediaOutput, 1)}
	taskRunner := schedulerBackgroundTaskRunner{
		tasks:   make(chan core.BackgroundCodexTask, 1),
		release: make(chan struct{}),
	}
	runner := backgroundTaskRunner{
		tasks: []core.BackgroundCodexTask{{
			Instruction:       "create flower",
			ExpectedArtifacts: []string{"image/jpeg"},
		}},
	}
	scheduler := NewScheduler(context.Background(), store, runner)
	scheduler.SetBackgroundCodexTaskRunner(taskRunner)
	scheduler.SetBackgroundArtifactStore(schedulerArtifactStore{})

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}
	if !store.completed {
		t.Fatal("completed = false, want true")
	}
	if store.completedResult.FinalOutput != "收到，开始处理。" {
		t.Fatalf("final output = %q", store.completedResult.FinalOutput)
	}

	task := <-taskRunner.tasks
	if task.Instruction != "create flower" {
		t.Fatalf("task instruction = %q", task.Instruction)
	}
	select {
	case media := <-store.mediaDeliveries:
		t.Fatalf("media delivery created before background task released: %+v", media)
	default:
	}

	close(taskRunner.release)
	select {
	case media := <-store.mediaDeliveries:
		if media.MediaID != "gm_async" {
			t.Fatalf("media id = %q, want gm_async", media.MediaID)
		}
	case <-time.After(time.Second):
		t.Fatal("async media delivery was not created")
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

func TestRunOnceDoesNotSelectPrivateMentionedAgents(t *testing.T) {
	store := &fakeStore{
		contextMessages: []core.Message{
			{ID: 1, Payload: []byte(`{"text":"@private @shared 帮忙看下"}`)},
		},
		agents: []core.Agent{
			{ID: 1, Key: "private", DisplayName: "Private", Prompt: "Private prompt.", Visibility: "private", Enabled: true},
			{ID: 2, Key: "shared", DisplayName: "Shared", Prompt: "Shared prompt.", Visibility: "shared", Enabled: true},
		},
	}
	runner := &contextRunner{}
	scheduler := NewScheduler(context.Background(), store, runner)

	if !scheduler.RunOnce(context.Background()) {
		t.Fatal("RunOnce = false, want true")
	}
	if len(runner.lastRun.SelectedAgents) != 1 {
		t.Fatalf("selected agents = %+v, want shared only", runner.lastRun.SelectedAgents)
	}
	if runner.lastRun.SelectedAgents[0].Key != "shared" {
		t.Fatalf("selected agents = %+v, want shared only", runner.lastRun.SelectedAgents)
	}
}

type concurrentStore struct {
	mu     sync.Mutex
	claims int
	owners chan string
}

func (s *concurrentStore) ClaimNextAgentRun(_ context.Context, owner string, _ time.Duration) (core.AgentRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims >= 2 {
		return core.AgentRun{}, false, nil
	}
	s.claims++
	s.owners <- owner
	return core.AgentRun{
		AgentSessionID:      int64(100 + s.claims),
		RoomID:              int64(10 + s.claims),
		SourceMessageFromID: int64(20 + s.claims),
		SourceMessageToID:   int64(20 + s.claims),
		LockOwner:           owner,
	}, true, nil
}

func (s *concurrentStore) ListAgentRunMessages(context.Context, core.AgentRun) ([]core.Message, error) {
	return nil, nil
}

func (s *concurrentStore) CompleteAgentRun(context.Context, core.AgentRun, core.AgentRunResult) (*core.Delivery, error) {
	return nil, nil
}

func (s *concurrentStore) FailAgentRun(context.Context, core.AgentRun, string) (*core.Delivery, error) {
	return nil, nil
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r blockingRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	r.started <- struct{}{}
	<-r.release
	return core.AgentRunResult{FinalOutput: "done"}, nil
}

func TestRunWorkersUsesDistinctLockOwners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &concurrentStore{owners: make(chan string, 2)}
	runner := blockingRunner{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	scheduler := NewScheduler(ctx, store, runner)
	scheduler.pollInterval = time.Millisecond

	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.RunWorkers(2)
	}()

	firstOwner := <-store.owners
	secondOwner := <-store.owners
	if firstOwner == secondOwner {
		t.Fatalf("worker owners are equal: %q", firstOwner)
	}
	<-runner.started
	<-runner.started

	cancel()
	close(runner.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunWorkers did not stop after context cancellation")
	}
}
