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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	httpapi "tinyclaw/internal/api"
	"tinyclaw/internal/command"
	"tinyclaw/internal/envfile"
	"tinyclaw/internal/executor"
	"tinyclaw/internal/storage"
)

const dbStartupTimeout = 10 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := envfile.Load(".env"); err != nil {
		slog.Warn("load .env failed", "err", err)
	}

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
	defer func() { _ = store.Close() }()
	if err := store.InitSchema(ctx); err != nil {
		cancel()
		slog.Error("init postgres schema failed", "err", err)
		os.Exit(1)
	}
	coreStore := storage.NewCoreStore(store.DB())
	cancel()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agentScheduler := executor.NewScheduler(runCtx, coreStore, buildAgentRunner(cfg))
	agentScheduler.SetMemorySearchURL(memorySearchEndpoint(cfg.ControlAPIAddr))
	memoryWriteWorker := executor.NewMemoryWriteWorker(runCtx, coreStore)
	commandHandler := buildCommandHandler(cfg, coreStore)
	coreAPI := httpapi.NewServerWithCommandHandler(coreStore, commandHandler, cfg.ClawmanAPIToken, cfg.ClawmanAdminSecret)
	controlHandler := withAdminUI(coreAPI, "web/control/dist")

	// Start metrics server
	go agentScheduler.RunLoop()
	go memoryWriteWorker.RunLoop()
	go serveMetrics(runCtx, cfg.MetricsAddr)
	go serveCoreAPI(runCtx, cfg.ControlAPIAddr, controlHandler)

	<-runCtx.Done()

	slog.Info("clawman stopped")
}

func withAdminUI(api http.Handler, distDir string) http.Handler {
	files := http.FileServer(http.Dir(distDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/") && !strings.HasPrefix(r.URL.Path, "/admin/api/") {
			relativePath := strings.TrimPrefix(r.URL.Path, "/admin/")
			if shouldServeAdminFile(distDir, relativePath) {
				http.StripPrefix("/admin/", files).ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
			return
		}
		api.ServeHTTP(w, r)
	})
}

func shouldServeAdminFile(distDir string, relativePath string) bool {
	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || cleanPath == string(filepath.Separator) {
		return true
	}
	if strings.HasPrefix(cleanPath, "..") {
		return false
	}
	info, err := os.Stat(filepath.Join(distDir, cleanPath))
	if err != nil {
		return strings.HasPrefix(cleanPath, "assets"+string(filepath.Separator))
	}
	return !info.IsDir()
}

func buildCommandHandler(cfg Config, coreStore *storage.CoreStore) *command.Handler {
	var image command.ImageGenerator
	if strings.TrimSpace(cfg.ImageProviderAPIKey) != "" {
		image = command.OpenAIImageClient{
			BaseURL: cfg.ImageProviderBaseURL,
			APIKey:  cfg.ImageProviderAPIKey,
			Model:   cfg.ImageProviderModel,
		}
	}
	var media command.MediaStore
	if strings.TrimSpace(cfg.GeneratedMediaS3Endpoint) != "" ||
		strings.TrimSpace(cfg.GeneratedMediaS3Bucket) != "" ||
		strings.TrimSpace(cfg.GeneratedMediaS3AccessKeyID) != "" ||
		strings.TrimSpace(cfg.GeneratedMediaS3SecretAccessKey) != "" {
		store, err := command.NewS3MediaStore(command.S3MediaStoreConfig{
			Endpoint:        cfg.GeneratedMediaS3Endpoint,
			Bucket:          cfg.GeneratedMediaS3Bucket,
			Region:          cfg.GeneratedMediaS3Region,
			AccessKeyID:     cfg.GeneratedMediaS3AccessKeyID,
			SecretAccessKey: cfg.GeneratedMediaS3SecretAccessKey,
			ForcePathStyle:  cfg.GeneratedMediaS3ForcePathStyle,
			URLTTL:          cfg.GeneratedMediaURLTTL,
		})
		if err != nil {
			slog.Warn("generated media s3 store disabled", "err", err)
		} else {
			media = store
		}
	}
	handler := command.NewHandler(coreStore, image, media)
	handler.Enabled = cfg.DrawCommandEnabled
	handler.ImageSize = cfg.DrawImageSize
	handler.MediaURLTTL = cfg.GeneratedMediaURLTTL
	return handler
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
