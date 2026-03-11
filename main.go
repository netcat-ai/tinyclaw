package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"k8s.io/client-go/rest"
	sandboxclient "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
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
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("k8s in-cluster config failed", "err", err)
		os.Exit(1)
	}
	clientset, err := sandboxclient.NewForConfig(k8sCfg)
	if err != nil {
		slog.Error("k8s clientset init failed", "err", err)
		os.Exit(1)
	}
	orch = sandbox.NewOrchestrator(clientset, redisClient, sandbox.Config{
		Namespace:       cfg.SandboxNamespace,
		Image:           cfg.SandboxImage,
		RedisAddr:       cfg.RedisAddr,
		ModelAPIBaseURL: cfg.ModelAPIBaseURL,
		ModelAPIKey:     cfg.ModelAPIKey,
	})
	slog.Info("sandbox orchestrator enabled", "namespace", cfg.SandboxNamespace, "image", cfg.SandboxImage)

	// Create egress consumer (nil if worktool not configured)
	var egress *EgressConsumer
	if cfg.WorkToolRobotID != "" {
		wt := worktool.NewClient(cfg.WorkToolRobotID)
		egress = NewEgressConsumer(redisClient, wt)
	}

	clawman, err := NewClawman(cfg, redisClient, orch, egress)
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
