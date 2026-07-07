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

	"tinyclaw/internal/envfile"
	"tinyclaw/internal/msghub"
)

const defaultAddr = ":8082"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := envfile.Load(".env"); err != nil {
		slog.Warn("load .env failed", "err", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	store, seedActor, cleanup := openStore(ctx)
	cancel()
	defer cleanup()
	seedActors(context.Background(), seedActor)

	handler := msghub.NewServer(store)
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := strings.TrimSpace(os.Getenv("MSGHUB_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}
	serve(runCtx, addr, handler)
}

func openStore(ctx context.Context) (msghub.Store, func(context.Context, msghub.Actor) error, func()) {
	dsn := strings.TrimSpace(os.Getenv("MSGHUB_DATABASE_URL"))
	if dsn == "" {
		store := msghub.NewMemoryStore()
		return store, func(_ context.Context, actor msghub.Actor) error {
			store.UpsertActor(actor)
			return nil
		}, func() {}
	}
	store, err := msghub.OpenPostgresStore(ctx, dsn)
	if err != nil {
		slog.Error("open msghub postgres store failed", "err", err)
		os.Exit(1)
	}
	if err := store.InitSchema(ctx); err != nil {
		_ = store.Close()
		slog.Error("init msghub postgres schema failed", "err", err)
		os.Exit(1)
	}
	return store, func(ctx context.Context, actor msghub.Actor) error {
			_, err := store.SeedActor(ctx, actor)
			return err
		}, func() {
			if err := store.Close(); err != nil {
				slog.Warn("close msghub postgres store failed", "err", err)
			}
		}
}

func seedActors(ctx context.Context, seedActor func(context.Context, msghub.Actor) error) {
	terminalToken := strings.TrimSpace(os.Getenv("MSGHUB_TERMINAL_TOKEN"))
	if terminalToken != "" {
		if err := seedActor(ctx, msghub.Actor{
			ActorKey:          envOrDefault("MSGHUB_TERMINAL_ACTOR_KEY", "terminal"),
			Token:             terminalToken,
			Kind:              msghub.ActorKindTerminal,
			Name:              envOrDefault("MSGHUB_TERMINAL_NAME", "Terminal Agent"),
			Channel:           envOrDefault("MSGHUB_TERMINAL_CHANNEL", "wechat"),
			ProviderAccountID: envOrDefault("MSGHUB_TERMINAL_PROVIDER_ACCOUNT_ID", "default"),
		}); err != nil {
			slog.Error("seed terminal actor failed", "err", err)
			os.Exit(1)
		}
	}
	aiToken := strings.TrimSpace(os.Getenv("MSGHUB_AIAGENT_TOKEN"))
	if aiToken != "" {
		if err := seedActor(ctx, msghub.Actor{
			ActorKey:    envOrDefault("MSGHUB_AIAGENT_ACTOR_KEY", "aiagent"),
			Token:       aiToken,
			Kind:        msghub.ActorKindAIAgent,
			Name:        envOrDefault("MSGHUB_AIAGENT_NAME", "AI Agent"),
			Permissions: []string{msghub.PermissionSubscribeAll},
		}); err != nil {
			slog.Error("seed aiagent actor failed", "err", err)
			os.Exit(1)
		}
	}
	if terminalToken == "" || aiToken == "" {
		slog.Warn("msghub started without both default actors; set MSGHUB_TERMINAL_TOKEN and MSGHUB_AIAGENT_TOKEN for manual testing")
	}
}

func serve(ctx context.Context, addr string, handler http.Handler) {
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

	slog.Info("msghub ilink api starting", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("msghub ilink api failed", "err", err)
	}
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
