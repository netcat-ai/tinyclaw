package executor

import (
	"context"
	"log/slog"
	"time"

	"tinyclaw/internal/core"
)

const defaultMemoryWorkerInterval = time.Second

type MemoryWriteJobStore interface {
	ApplyNextMemoryWriteJob(ctx context.Context) (core.MemoryWriteJob, bool, error)
}

type MemoryWriteWorker struct {
	ctx          context.Context
	store        MemoryWriteJobStore
	pollInterval time.Duration
}

func NewMemoryWriteWorker(ctx context.Context, store MemoryWriteJobStore) *MemoryWriteWorker {
	return &MemoryWriteWorker{
		ctx:          ctx,
		store:        store,
		pollInterval: defaultMemoryWorkerInterval,
	}
}

func (w *MemoryWriteWorker) RunLoop() {
	if w == nil || w.store == nil {
		return
	}
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		w.RunAvailable(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *MemoryWriteWorker) RunAvailable(ctx context.Context) {
	for {
		ran := w.RunOnce(ctx)
		if !ran {
			return
		}
	}
}

func (w *MemoryWriteWorker) RunOnce(ctx context.Context) bool {
	if w == nil || w.store == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job, ok, err := w.store.ApplyNextMemoryWriteJob(ctx)
	if err != nil {
		slog.Error("apply memory write job failed", "memory_write_job_id", job.ID, "err", err)
		return ok
	}
	return ok
}
