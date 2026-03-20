package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	msgPulled = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tinyclaw_messages_pulled_total",
		Help: "Total messages pulled from WeChat Work archive.",
	})
	msgDispatched = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tinyclaw_messages_dispatched_total",
		Help: "Total messages dispatched to sandbox agent.",
	})
	msgSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_messages_skipped_total",
		Help: "Total messages skipped by reason.",
	}, []string{"reason"})
	sandboxInvocations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_sandbox_invocations_total",
		Help: "Total sandbox invocations by result.",
	}, []string{"result"})
	sandboxDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "tinyclaw_sandbox_duration_seconds",
		Help:    "Sandbox invocation latency.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120},
	})
	dbOperations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tinyclaw_db_duration_seconds",
		Help:    "Database operation latency.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"operation"})
	deliveriesProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_deliveries_total",
		Help: "Total outbox deliveries by result.",
	}, []string{"result"})
	pullCycleErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tinyclaw_pull_cycle_errors_total",
		Help: "Total pull cycle errors.",
	})
	activeSandboxes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "tinyclaw_active_sandboxes",
		Help: "Number of active sandbox clients.",
	})
)

func init() {
	prometheus.MustRegister(
		msgPulled,
		msgDispatched,
		msgSkipped,
		sandboxInvocations,
		sandboxDuration,
		dbOperations,
		deliveriesProcessed,
		pullCycleErrors,
		activeSandboxes,
	)
}

func serveMetrics(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("metrics server starting", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("metrics server failed", "err", err)
	}
}

// dbTimer returns a function that records the duration of a DB operation when called.
func dbTimer(operation string) func() {
	start := time.Now()
	return func() {
		dbOperations.WithLabelValues(operation).Observe(time.Since(start).Seconds())
	}
}
