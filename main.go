package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"tinyclaw/sandbox"
	"tinyclaw/worktool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	if cfg.WeComBotID == "" {
		slog.Warn("WECOM_BOT_ID is empty; bot-sent direct messages will be ingested as room_id=<bot_id> and may create self-loop sandboxes")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("k8s in-cluster config failed", "err", err)
		os.Exit(1)
	}
	orch := sandbox.NewOrchestrator(sandbox.Config{
		Namespace:    cfg.SandboxNamespace,
		TemplateName: cfg.SandboxTemplateName,
		ServerPort:   cfg.SandboxServerPort,
		ReadyTimeout: time.Duration(cfg.SandboxReadyTimeoutSec) * time.Second,
		RestConfig:   k8sCfg,
	})
	slog.Info(
		"sandbox sdk integration enabled",
		"namespace", cfg.SandboxNamespace,
		"template", cfg.SandboxTemplateName,
		"connect_mode", "port-forward",
		"server_port", cfg.SandboxServerPort,
	)

	// Create egress consumer (nil if worktool not configured)
	var egress *EgressConsumer
	if cfg.WorkToolRobotID != "" {
		wt := worktool.NewClient(cfg.WorkToolRobotID)
		egress = NewEgressConsumer(store, wt)
	}

	clawman, err := NewClawman(cfg, store, orch, egress)
	if err != nil {
		slog.Error("init clawman failed", "err", err)
		os.Exit(1)
	}
	defer clawman.Close()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start egress consumer goroutine
	if egress != nil {
		go func() {
			slog.Info("egress consumer starting")
			if err := egress.Run(runCtx); err != nil {
				slog.Error("egress consumer failed", "err", err)
			}
		}()
	}

	if err := clawman.Run(runCtx); err != nil {
		slog.Error("clawman stopped with error", "err", err)
		os.Exit(1)
	}

	slog.Info("clawman stopped")
}
