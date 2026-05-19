package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"tinyclaw/internal/core"
)

type Store interface {
	StartCoreInvocation(ctx context.Context, invocationID int64) (core.Invocation, error)
	ListCoreInvocationContextMessages(ctx context.Context, invocationID int64) ([]core.Message, error)
	ReadCoreInvocationNewMessages(ctx context.Context, invocationID int64) ([]core.Message, error)
	CompleteCoreInvocation(ctx context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error)
	FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (core.InvocationResult, error)
}

type Runner interface {
	RunInvocation(ctx context.Context, run InvocationRun) (string, error)
}

type InvocationRun struct {
	Invocation      core.Invocation
	ContextMessages []core.Message
	readNewMessages func(context.Context) ([]core.Message, error)
}

func (r InvocationRun) ReadNewMessages(ctx context.Context) ([]core.Message, error) {
	if r.readNewMessages == nil {
		return nil, nil
	}
	return r.readNewMessages(ctx)
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

	contextMessages, err := s.store.ListCoreInvocationContextMessages(ctx, invocation.ID)
	if err != nil {
		s.failInvocation(ctx, invocation.ID, err)
		return
	}

	run := InvocationRun{
		Invocation:      invocation,
		ContextMessages: contextMessages,
		readNewMessages: func(readCtx context.Context) ([]core.Message, error) {
			if readCtx == nil {
				readCtx = ctx
			}
			return s.store.ReadCoreInvocationNewMessages(readCtx, invocation.ID)
		},
	}

	output, err := s.runner.RunInvocation(ctx, run)
	if err != nil {
		s.failInvocation(ctx, invocation.ID, err)
		return
	}
	if _, err := s.store.CompleteCoreInvocation(ctx, invocation.ID, core.CompleteInvocationInput{Text: output}); err != nil {
		slog.Error("complete invocation failed", "invocation_id", invocation.ID, "err", err)
	}
}

func (s *Scheduler) failInvocation(ctx context.Context, invocationID int64, err error) {
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		detail = "执行失败，请稍后重试"
	}
	if _, failErr := s.store.FailCoreInvocation(ctx, invocationID, detail); failErr != nil {
		slog.Error("fail invocation failed", "invocation_id", invocationID, "err", failErr)
	}
}

type UnconfiguredRunner struct{}

func (UnconfiguredRunner) RunInvocation(context.Context, InvocationRun) (string, error) {
	return "", fmt.Errorf("agent executor 未配置")
}

type StaticRunner struct {
	Text string
}

func (r StaticRunner) RunInvocation(context.Context, InvocationRun) (string, error) {
	return r.Text, nil
}
