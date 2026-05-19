package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"tinyclaw/internal/core"
)

type Store interface {
	StartCoreInvocation(ctx context.Context, invocationID int64) (core.Invocation, error)
	CompleteCoreInvocation(ctx context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error)
	FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (core.InvocationResult, error)
}

type Runner interface {
	RunInvocation(ctx context.Context, invocation core.Invocation) (core.CompleteInvocationInput, error)
}

type Scheduler struct {
	ctx    context.Context
	store  Store
	runner Runner
}

func NewScheduler(ctx context.Context, store Store, runner Runner) *Scheduler {
	return &Scheduler{
		ctx:    ctx,
		store:  store,
		runner: runner,
	}
}

func (s *Scheduler) ScheduleInvocation(invocationID int64) {
	if s == nil || s.store == nil || s.runner == nil || invocationID <= 0 {
		return
	}
	go s.RunOnce(s.ctx, invocationID)
}

func (s *Scheduler) RunOnce(ctx context.Context, invocationID int64) {
	if s == nil || s.store == nil || s.runner == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	invocation, err := s.store.StartCoreInvocation(ctx, invocationID)
	if err != nil {
		slog.Error("start invocation failed", "invocation_id", invocationID, "err", err)
		return
	}

	output, err := s.runner.RunInvocation(ctx, invocation)
	if err != nil {
		detail := strings.TrimSpace(err.Error())
		if detail == "" {
			detail = "执行失败，请稍后重试"
		}
		if _, failErr := s.store.FailCoreInvocation(ctx, invocation.ID, detail); failErr != nil {
			slog.Error("fail invocation failed", "invocation_id", invocation.ID, "err", failErr)
		}
		return
	}
	if _, err := s.store.CompleteCoreInvocation(ctx, invocation.ID, output); err != nil {
		slog.Error("complete invocation failed", "invocation_id", invocation.ID, "err", err)
	}
}

type UnconfiguredRunner struct{}

func (UnconfiguredRunner) RunInvocation(context.Context, core.Invocation) (core.CompleteInvocationInput, error) {
	return core.CompleteInvocationInput{}, fmt.Errorf("agent executor 未配置")
}

type StaticRunner struct {
	Text string
}

func (r StaticRunner) RunInvocation(context.Context, core.Invocation) (core.CompleteInvocationInput, error) {
	return core.CompleteInvocationInput{
		Output: json.RawMessage(`{"status":"completed","source":"static_runner"}`),
		Text:   r.Text,
	}, nil
}
