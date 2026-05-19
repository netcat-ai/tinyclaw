package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	httpapi "tinyclaw/internal/api"
)

func serveControlAPI(ctx context.Context, cfg Config, coreAPI *httpapi.Server) {
	mux := newControlMux(coreAPI)

	srv := &http.Server{
		Addr:              cfg.ControlAPIAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("control api starting", "addr", cfg.ControlAPIAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("control api failed", "err", err)
	}
}

func newControlMux(coreAPI *httpapi.Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if coreAPI != nil {
		coreAPI.RegisterCoreRoutes(mux)
	}
	return mux
}
