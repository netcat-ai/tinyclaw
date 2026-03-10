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
	sandboxclient "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
	"tinyclaw/sandbox"
	"tinyclaw/worktool"
)

// portFromAddr extracts the port from an address like ":8080" or "0.0.0.0:8080".
func portFromAddr(addr string) string {
	if i := len(addr) - 1; i >= 0 {
		for i >= 0 && addr[i] != ':' {
			i--
		}
		if i >= 0 {
			return addr[i+1:]
		}
	}
	return addr
}

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
	if cfg.SandboxEnabled {
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
			StreamPrefix:    cfg.StreamPrefix,
			EgressBaseURL:   "http://clawman-svc:" + portFromAddr(cfg.EgressAddr) + "/egress",
			EgressToken:     cfg.EgressToken,
			ModelAPIBaseURL: cfg.ModelAPIBaseURL,
			ModelAPIKey:     cfg.ModelAPIKey,
		})
		slog.Info("sandbox orchestrator enabled", "namespace", cfg.SandboxNamespace, "image", cfg.SandboxImage)
	}

	clawman, err := NewClawman(cfg, redisClient, orch)
	if err != nil {
		slog.Error("init clawman failed", "err", err)
		os.Exit(1)
	}
	defer clawman.Close()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start egress HTTP server if worktool is configured
	var egressSrv *http.Server
	if cfg.WorkToolRobotID != "" && cfg.EgressToken != "" {
		wt := worktool.NewClient(cfg.WorkToolRobotID)
		egress := NewEgressServer(cfg.EgressToken, redisClient, wt)
		egressSrv = &http.Server{Addr: cfg.EgressAddr, Handler: egress}
		go func() {
			slog.Info("egress server starting", "addr", cfg.EgressAddr)
			if err := egressSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("egress server failed", "err", err)
			}
		}()
	}

	if err := clawman.Run(runCtx); err != nil {
		slog.Error("clawman stopped with error", "err", err)
		os.Exit(1)
	}

	if egressSrv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		egressSrv.Shutdown(shutdownCtx)
	}

	slog.Info("clawman stopped")
}
