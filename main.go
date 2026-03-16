package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"k8s.io/client-go/rest"
	extensionsclient "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
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
	if cfg.SandboxTemplateName == "" {
		slog.Error("SANDBOX_TEMPLATE_NAME is required")
		os.Exit(1)
	}
	if cfg.SandboxRouterURL == "" {
		slog.Error("SANDBOX_ROUTER_URL is required")
		os.Exit(1)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer redisClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		cancel()
		slog.Error("redis ping failed", "err", err)
		os.Exit(1)
	}
	cancel()

	var orch *sandbox.Orchestrator
	var routerClient *sandbox.RouterClient
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("k8s in-cluster config failed", "err", err)
		os.Exit(1)
	}
	clientset, err := extensionsclient.NewForConfig(k8sCfg)
	if err != nil {
		slog.Error("agent sandbox extensions clientset init failed", "err", err)
		os.Exit(1)
	}
	orch = sandbox.NewOrchestrator(clientset, redisClient, sandbox.Config{
		Namespace:    cfg.SandboxNamespace,
		TemplateName: cfg.SandboxTemplateName,
		ReadyTimeout: time.Duration(cfg.SandboxReadyTimeoutSec) * time.Second,
	})
	routerClient = sandbox.NewRouterClient(http.DefaultClient, sandbox.RouterConfig{
		BaseURL:    cfg.SandboxRouterURL,
		Namespace:  cfg.SandboxNamespace,
		ServerPort: cfg.SandboxServerPort,
	})
	slog.Info(
		"sandbox integration enabled",
		"namespace", cfg.SandboxNamespace,
		"template", cfg.SandboxTemplateName,
		"router_url", cfg.SandboxRouterURL,
		"server_port", cfg.SandboxServerPort,
	)

	// Create egress consumer (nil if worktool not configured)
	var egress *EgressConsumer
	if cfg.WorkToolRobotID != "" {
		wt := worktool.NewClient(cfg.WorkToolRobotID)
		egress = NewEgressConsumer(redisClient, wt)
	}

	clawman, err := NewClawman(cfg, redisClient, orch, routerClient, egress)
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
