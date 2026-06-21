package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"tinyclaw/internal/core"
	"tinyclaw/internal/telemetry"
)

const (
	defaultPollInterval   = time.Second
	defaultLockTTL        = 5 * time.Minute
	defaultMemoryTokenTTL = 10 * time.Minute
)

type Store interface {
	ClaimNextAgentRun(ctx context.Context, owner string, ttl time.Duration) (core.AgentRun, bool, error)
	ListAgentRunMessages(ctx context.Context, run core.AgentRun) ([]core.Message, error)
	CompleteAgentRun(ctx context.Context, run core.AgentRun, result core.AgentRunResult) (*core.Delivery, error)
	FailAgentRun(ctx context.Context, run core.AgentRun, detail string) (*core.Delivery, error)
}

type GeneratedMediaDeliveryStore interface {
	CreateGeneratedMediaDelivery(ctx context.Context, run core.AgentRun, media core.GeneratedMediaOutput) (*core.Delivery, error)
}

type BackgroundCodexTaskRunner interface {
	RunBackgroundCodexTask(ctx context.Context, run AgentRunRequest, task core.BackgroundCodexTask) (core.BackgroundCodexTaskResult, error)
}

type BackgroundArtifactStore interface {
	StoreBackgroundArtifact(ctx context.Context, artifact core.BackgroundArtifact) (core.GeneratedMediaOutput, error)
}

type MemoryCapabilityTokenStore interface {
	CreateMemoryCapabilityToken(ctx context.Context, run core.AgentRun, ttl time.Duration) (string, error)
}

type AgentDefinitionStore interface {
	ListAgents(ctx context.Context) ([]core.Agent, error)
}

type Runner interface {
	RunAgent(ctx context.Context, run AgentRunRequest) (core.AgentRunResult, error)
}

type AgentRunRequest struct {
	AgentRun            core.AgentRun
	ContextMessages     []core.Message
	SelectedAgents      []core.Agent
	MediaBaseURL        string
	MemorySearchURL     string
	MemorySearchToken   string
	MemorySearchResults []core.MemorySearchResult
}

type Scheduler struct {
	ctx             context.Context
	store           Store
	runner          Runner
	owner           string
	pollInterval    time.Duration
	lockTTL         time.Duration
	memorySearchURL string
	memoryTokenTTL  time.Duration
	taskRunner      BackgroundCodexTaskRunner
	artifactStore   BackgroundArtifactStore
}

func NewScheduler(ctx context.Context, store Store, runner Runner) *Scheduler {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "clawman"
	}
	return &Scheduler{
		ctx:            ctx,
		store:          store,
		runner:         runner,
		owner:          fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		pollInterval:   defaultPollInterval,
		lockTTL:        defaultLockTTL,
		memoryTokenTTL: defaultMemoryTokenTTL,
	}
}

func (s *Scheduler) SetMemorySearchURL(url string) {
	if s != nil {
		s.memorySearchURL = strings.TrimSpace(url)
	}
}

func (s *Scheduler) SetBackgroundCodexTaskRunner(runner BackgroundCodexTaskRunner) {
	if s != nil {
		s.taskRunner = runner
	}
}

func (s *Scheduler) SetBackgroundArtifactStore(store BackgroundArtifactStore) {
	if s != nil {
		s.artifactStore = store
	}
}

func (s *Scheduler) RunLoop() {
	s.runLoopWithOwner(s.owner)
}

func (s *Scheduler) RunWorkers(concurrency int) {
	if s == nil || s.store == nil || s.runner == nil {
		return
	}
	if concurrency <= 1 {
		s.RunLoop()
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workerOwner := fmt.Sprintf("%s-w%d", s.owner, i+1)
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			s.runLoopWithOwner(owner)
		}(workerOwner)
	}
	wg.Wait()
}

func (s *Scheduler) runLoopWithOwner(owner string) {
	if s == nil || s.store == nil || s.runner == nil {
		return
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		s.runAvailableWithOwner(ctx, owner)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) RunAvailable(ctx context.Context) {
	s.runAvailableWithOwner(ctx, s.owner)
}

func (s *Scheduler) runAvailableWithOwner(ctx context.Context, owner string) {
	for {
		ran := s.runOnceWithOwner(ctx, owner)
		if !ran {
			return
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) bool {
	return s.runOnceWithOwner(ctx, s.owner)
}

func (s *Scheduler) runOnceWithOwner(ctx context.Context, owner string) bool {
	if s == nil || s.store == nil || s.runner == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run, ok, err := s.store.ClaimNextAgentRun(ctx, owner, s.lockTTL)
	if err != nil {
		telemetry.IncAgentRun("claim_error")
		slog.Error("claim agent run failed", "err", err)
		return false
	}
	if !ok {
		return false
	}

	contextMessages, err := s.store.ListAgentRunMessages(ctx, run)
	if err != nil {
		telemetry.IncAgentRun("context_error")
		s.failAgentRun(ctx, run, err)
		return true
	}

	request := AgentRunRequest{
		AgentRun:        run,
		ContextMessages: contextMessages,
		SelectedAgents:  s.selectedAgentsForRun(ctx, contextMessages),
		MemorySearchURL: s.memorySearchURL,
	}
	selectedAgentIDs, selectedAgentKeys := selectedAgentLogFields(request.SelectedAgents)
	slog.Info("agent run started",
		"agent_session_id", run.AgentSessionID,
		"room_id", run.RoomID,
		"source_message_from_id", run.SourceMessageFromID,
		"source_message_to_id", run.SourceMessageToID,
		"context_message_count", len(contextMessages),
		"selected_agent_ids", selectedAgentIDs,
		"selected_agent_keys", selectedAgentKeys,
	)
	if s.memorySearchURL != "" {
		if tokenStore, ok := s.store.(MemoryCapabilityTokenStore); ok {
			token, err := tokenStore.CreateMemoryCapabilityToken(ctx, run, s.memoryTokenTTL)
			if err != nil {
				slog.Warn("create memory capability token failed", "agent_session_id", run.AgentSessionID, "err", err)
			} else {
				request.MemorySearchToken = token
			}
		}
	}
	result, err := s.runner.RunAgent(ctx, request)
	if err != nil {
		telemetry.IncAgentRun("runner_error")
		s.failAgentRun(ctx, run, err)
		slog.Error("agent run failed",
			"agent_session_id", run.AgentSessionID,
			"room_id", run.RoomID,
			"selected_agent_ids", selectedAgentIDs,
			"selected_agent_keys", selectedAgentKeys,
			"err", err,
		)
		return true
	}
	if len(result.BackgroundCodexTasks) > 0 && !s.canRunBackgroundTasks() {
		result.FinalOutput = "后台任务能力未配置"
		result.BackgroundCodexTasks = nil
		result.BackgroundTaskCount = 0
	}
	if len(result.BackgroundCodexTasks) > 0 && strings.TrimSpace(result.FinalOutput) == "" {
		result.FinalOutput = "收到，开始处理。"
	}
	delivery, err := s.store.CompleteAgentRun(ctx, run, result)
	if err != nil {
		telemetry.IncAgentRun("complete_error")
		slog.Error("complete agent run failed", "agent_session_id", run.AgentSessionID, "err", err)
		return true
	}
	s.runBackgroundCodexTasksAsync(request, result.BackgroundCodexTasks)
	deliveryID := int64(0)
	if delivery != nil {
		deliveryID = delivery.ID
	}
	slog.Info("agent run completed",
		"agent_session_id", run.AgentSessionID,
		"room_id", run.RoomID,
		"selected_agent_ids", selectedAgentIDs,
		"selected_agent_keys", selectedAgentKeys,
		"memory_search_count", result.MemorySearchCount,
		"memory_write_job_count", len(result.MemoryWriteProposals),
		"background_task_count", result.BackgroundTaskCount,
		"delivery_id", deliveryID,
	)
	telemetry.IncAgentRun("success")
	return true
}

func (s *Scheduler) canRunBackgroundTasks() bool {
	if s == nil || s.taskRunner == nil || s.artifactStore == nil {
		return false
	}
	_, ok := s.store.(GeneratedMediaDeliveryStore)
	return ok
}

func (s *Scheduler) runBackgroundCodexTasksAsync(run AgentRunRequest, tasks []core.BackgroundCodexTask) {
	if len(tasks) == 0 {
		return
	}
	deliveryStore, ok := s.store.(GeneratedMediaDeliveryStore)
	if !ok {
		slog.Error("background task delivery store is not configured", "agent_session_id", run.AgentRun.AgentSessionID)
		return
	}
	if s.taskRunner == nil || s.artifactStore == nil {
		slog.Error("background task dependencies are not configured", "agent_session_id", run.AgentRun.AgentSessionID)
		return
	}
	tasks = append([]core.BackgroundCodexTask(nil), tasks...)
	go func() {
		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, defaultCodexRunnerTimeout)
		defer cancel()
		for _, task := range tasks {
			slog.Info("background codex task started",
				"agent_session_id", run.AgentRun.AgentSessionID,
				"room_id", run.AgentRun.RoomID,
				"source_message_ids", task.SourceMessageIDs,
				"instruction", truncateLogValue(task.Instruction, 500),
				"expected_artifacts", task.ExpectedArtifacts,
			)
			result, err := s.taskRunner.RunBackgroundCodexTask(ctx, run, task)
			if err != nil {
				slog.Error("background codex task failed",
					"agent_session_id", run.AgentRun.AgentSessionID,
					"room_id", run.AgentRun.RoomID,
					"source_message_ids", task.SourceMessageIDs,
					"instruction", task.Instruction,
					"err", err,
				)
				continue
			}
			if strings.TrimSpace(result.OutputDir) != "" {
				defer func(path string) {
					if err := os.RemoveAll(path); err != nil {
						slog.Warn("remove background task output dir failed", "path", path, "err", err)
					}
				}(result.OutputDir)
			}
			slog.Info("background codex task completed",
				"agent_session_id", run.AgentRun.AgentSessionID,
				"room_id", run.AgentRun.RoomID,
				"source_message_ids", task.SourceMessageIDs,
				"artifact_count", len(result.Artifacts),
			)
			for _, artifact := range result.Artifacts {
				output, err := s.artifactStore.StoreBackgroundArtifact(ctx, artifact)
				if err != nil {
					slog.Error("store background artifact failed",
						"agent_session_id", run.AgentRun.AgentSessionID,
						"room_id", run.AgentRun.RoomID,
						"artifact_path", artifact.Path,
						"err", err,
					)
					continue
				}
				delivery, err := deliveryStore.CreateGeneratedMediaDelivery(ctx, run.AgentRun, output)
				if err != nil {
					slog.Error("create background artifact delivery failed",
						"agent_session_id", run.AgentRun.AgentSessionID,
						"room_id", run.AgentRun.RoomID,
						"media_id", output.MediaID,
						"err", err,
					)
					continue
				}
				deliveryID := int64(0)
				if delivery != nil {
					deliveryID = delivery.ID
				}
				slog.Info("background artifact delivered",
					"agent_session_id", run.AgentRun.AgentSessionID,
					"room_id", run.AgentRun.RoomID,
					"media_id", output.MediaID,
					"delivery_id", deliveryID,
				)
			}
		}
	}()
}

func truncateLogValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func (s *Scheduler) selectedAgentsForRun(ctx context.Context, messages []core.Message) []core.Agent {
	store, ok := s.store.(AgentDefinitionStore)
	if !ok {
		return nil
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		slog.Warn("list agents for run failed", "err", err)
		return nil
	}
	return selectMentionedAgents(messages, agents)
}

func selectMentionedAgents(messages []core.Message, agents []core.Agent) []core.Agent {
	text := strings.ToLower(strings.Join(messageTexts(messages), "\n"))
	if strings.TrimSpace(text) == "" {
		return nil
	}
	selected := make([]core.Agent, 0, len(agents))
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		if agent.Visibility != "" && agent.Visibility != "shared" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(agent.Key))
		name := strings.ToLower(strings.TrimSpace(agent.DisplayName))
		if key != "" && strings.Contains(text, "@"+key) {
			selected = append(selected, agent)
			continue
		}
		if name != "" && strings.Contains(text, "@"+name) {
			selected = append(selected, agent)
		}
	}
	return selected
}

func selectedAgentLogFields(agents []core.Agent) ([]int64, []string) {
	ids := make([]int64, 0, len(agents))
	keys := make([]string, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.ID)
		key := strings.TrimSpace(agent.Key)
		if key == "" {
			key = strings.TrimSpace(agent.DisplayName)
		}
		if key != "" {
			keys = append(keys, key)
		}
	}
	return ids, keys
}

func (s *Scheduler) failAgentRun(ctx context.Context, run core.AgentRun, err error) {
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		detail = "执行失败，请稍后重试"
	}
	if _, failErr := s.store.FailAgentRun(ctx, run, detail); failErr != nil {
		slog.Error("fail agent run failed", "agent_session_id", run.AgentSessionID, "err", failErr)
	}
}

type UnconfiguredRunner struct{}

func (UnconfiguredRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{}, fmt.Errorf("agent executor 未配置")
}

type StaticRunner struct {
	Text                 string
	MemoryWriteProposals []core.MemoryWriteProposal
}

func (r StaticRunner) RunAgent(context.Context, AgentRunRequest) (core.AgentRunResult, error) {
	return core.AgentRunResult{
		FinalOutput:          r.Text,
		MemoryWriteProposals: r.MemoryWriteProposals,
	}, nil
}
