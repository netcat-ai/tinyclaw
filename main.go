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
		APIURL:       cfg.SandboxRouterURL,
		ServerPort:   cfg.SandboxServerPort,
		ReadyTimeout: time.Duration(cfg.SandboxReadyTimeoutSec) * time.Second,
		RestConfig:   k8sCfg,
	})
	slog.Info(
		"sandbox sdk integration enabled",
		"namespace", cfg.SandboxNamespace,
		"template", cfg.SandboxTemplateName,
		"connect_mode", "direct-url",
		"api_url", cfg.SandboxRouterURL,
		"server_port", cfg.SandboxServerPort,
	)

	// Create WorkTool client for direct reply delivery.
	var replyClient *worktool.Client
	if cfg.WorkToolRobotID != "" {
		replyClient = worktool.NewClient(cfg.WorkToolRobotID)
	} else {
		slog.Warn("WORKTOOL_ROBOT_ID is empty; agent replies will remain pending until outbound delivery is configured")
	}

	clawman, err := NewClawman(cfg, store, orch, replyClient)
	if err != nil {
		slog.Error("init clawman failed", "err", err)
		os.Exit(1)
	}
	defer clawman.Close()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start metrics server
	go serveMetrics(runCtx, cfg.MetricsAddr)
	go serveControlAPI(runCtx, cfg, store)

	if err := clawman.Run(runCtx); err != nil {
		slog.Error("clawman stopped with error", "err", err)
		os.Exit(1)
	}

	slog.Info("clawman stopped")
}
