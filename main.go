package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tinyclaw/channel/wecom"
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
	agentScheduler := executor.NewScheduler(runCtx, coreStore, buildAgentRunner(cfg))
	agentScheduler.SetMemorySearchURL(memorySearchEndpoint(cfg.ControlAPIAddr))
	memoryWriteWorker := executor.NewMemoryWriteWorker(runCtx, coreStore)
	coreAPI := httpapi.NewServer(coreStore, cfg.ClawmanAPIToken)

	// Start metrics server
	go agentScheduler.RunLoop()
	go memoryWriteWorker.RunLoop()
	go serveMetrics(runCtx, cfg.MetricsAddr)
	go serveCoreAPI(runCtx, cfg.ControlAPIAddr, coreAPI)
	if cfg.WeComEnabled {
		go serveWeComArchiveAdapter(runCtx, store, coreStore, cfg)
	}

	<-runCtx.Done()

	slog.Info("clawman stopped")
}

func memorySearchEndpoint(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return ""
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/internal/memory/search"
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/internal/memory/search"
}

func serveWeComArchiveAdapter(ctx context.Context, store *Store, coreStore *storage.CoreStore, cfg Config) {
	adapter := wecom.NewArchiveAdapter(store.DB(), coreStore, wecom.ArchiveConfig{
		CorpID:        cfg.WeComCorpID,
		CorpSecret:    cfg.WeComCorpSecret,
		ContactSecret: cfg.WeComContactSecret,
		RSAPrivateKey: cfg.WeComRSAPrivateKey,
		BotID:         cfg.WeComBotID,
		Proxy:         cfg.WeComProxy,
		ProxyPassword: cfg.WeComProxyPassword,
		PollInterval:  cfg.WeComPollInterval,
		PollLimit:     cfg.WeComPollLimit,
		SDKTimeout:    cfg.WeComSDKTimeout,
		StartSeq:      cfg.WeComStartSeq,
	})
	if err := adapter.Run(ctx); err != nil {
		slog.Error("wecom archive adapter stopped", "err", err)
	}
}

func buildAgentRunner(cfg Config) executor.Runner {
	switch strings.ToLower(strings.TrimSpace(cfg.AgentRunner)) {
	case "codex":
		return executor.NewCodexRunner(executor.CodexRunnerConfig{
			Bin:              cfg.CodexBin,
			WorkDir:          executor.AbsoluteCodexWorkDir(cfg.CodexWorkDir),
			Model:            cfg.CodexModel,
			Sandbox:          cfg.CodexSandbox,
			DisabledFeatures: cfg.CodexDisabledFeatures,
			Timeout:          cfg.CodexRunnerTimeout,
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
