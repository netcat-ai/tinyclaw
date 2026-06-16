package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

func (s *Scheduler) RunLoop() {
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
		s.RunAvailable(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) RunAvailable(ctx context.Context) {
	for {
		ran := s.RunOnce(ctx)
		if !ran {
			return
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) bool {
	if s == nil || s.store == nil || s.runner == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run, ok, err := s.store.ClaimNextAgentRun(ctx, s.owner, s.lockTTL)
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
	delivery, err := s.store.CompleteAgentRun(ctx, run, result)
	if err != nil {
		telemetry.IncAgentRun("complete_error")
		slog.Error("complete agent run failed", "agent_session_id", run.AgentSessionID, "err", err)
		return true
	}
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
		"delivery_id", deliveryID,
	)
	telemetry.IncAgentRun("success")
	return true
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
