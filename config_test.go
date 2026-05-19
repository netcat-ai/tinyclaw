package main

import "testing"

func TestLoadConfigUsesServiceDefaults(t *testing.T) {
	t.Setenv("CONTROL_API_ADDR", "")
	t.Setenv("METRICS_ADDR", "")
	t.Setenv("CODEX_RUNNER_TIMEOUT", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.ControlAPIAddr != ":8081" {
		t.Fatalf("ControlAPIAddr = %q, want :8081", cfg.ControlAPIAddr)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Fatalf("MetricsAddr = %q, want :9090", cfg.MetricsAddr)
	}
	if cfg.CodexBin != "codex" {
		t.Fatalf("CodexBin = %q, want codex", cfg.CodexBin)
	}
	if cfg.CodexRunnerTimeout.String() != "5m0s" {
		t.Fatalf("CodexRunnerTimeout = %s, want 5m0s", cfg.CodexRunnerTimeout)
	}
}
