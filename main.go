package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpapi "tinyclaw/internal/api"
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
	if err := store.ResetSentMessages(ctx); err != nil {
		cancel()
		slog.Error("reset sent messages failed", "err", err)
		os.Exit(1)
	}
	cancel()

	coreStore := storage.NewCoreStore(store.DB())
	coreAPI := httpapi.NewServer(coreStore, cfg.ClawmanAPIToken)

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start metrics server
	go serveMetrics(runCtx, cfg.MetricsAddr)
	go serveControlAPI(runCtx, cfg, store, coreAPI, nil)

	<-runCtx.Done()

	slog.Info("clawman stopped")
}
