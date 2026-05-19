package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httpapi "tinyclaw/internal/api"
	"tinyclaw/internal/executor"
	"tinyclaw/internal/storage"
)

const dbStartupTimeout = 10 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbStartupTimeout)
	store, err := OpenStore(ctx, cfg.DatabaseURL)
	if err != nil {
		cancel()
		slog.Error("open postgres store failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.InitSchema(ctx); err != nil {
		cancel()
		slog.Error("init postgres schema failed", "err", err)
		os.Exit(1)
	}
	cancel()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	coreStore := storage.NewCoreStore(store.DB())
	invocationScheduler := executor.NewScheduler(runCtx, coreStore, buildInvocationRunner(cfg))
	coreAPI := httpapi.NewServer(coreStore, cfg.ClawmanAPIToken, invocationScheduler)

	// Start metrics server
	go serveMetrics(runCtx, cfg.MetricsAddr)
	go serveCoreAPI(runCtx, cfg.ControlAPIAddr, coreAPI)

	<-runCtx.Done()

	slog.Info("clawman stopped")
}

func buildInvocationRunner(cfg Config) executor.Runner {
	switch strings.ToLower(strings.TrimSpace(cfg.AgentRunner)) {
	case "codex":
		return executor.NewCodexRunner(executor.CodexRunnerConfig{
			Bin:     cfg.CodexBin,
			WorkDir: executor.AbsoluteCodexWorkDir(cfg.CodexWorkDir),
			Model:   cfg.CodexModel,
			Sandbox: cfg.CodexSandbox,
			Timeout: cfg.CodexRunnerTimeout,
		})
	default:
		return executor.UnconfiguredRunner{}
	}
}

func serveCoreAPI(ctx context.Context, addr string, handler http.Handler) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("core api starting", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("core api failed", "err", err)
	}
}
