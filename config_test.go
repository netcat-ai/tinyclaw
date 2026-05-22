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
	wantDisabled := []string{"apps", "tool_suggest", "plugins"}
	if len(cfg.CodexDisabledFeatures) != len(wantDisabled) {
		t.Fatalf("CodexDisabledFeatures = %v, want %v", cfg.CodexDisabledFeatures, wantDisabled)
	}
	for i, want := range wantDisabled {
		if cfg.CodexDisabledFeatures[i] != want {
			t.Fatalf("CodexDisabledFeatures = %v, want %v", cfg.CodexDisabledFeatures, wantDisabled)
		}
	}
	if cfg.CodexRunnerTimeout.String() != "5m0s" {
		t.Fatalf("CodexRunnerTimeout = %s, want 5m0s", cfg.CodexRunnerTimeout)
	}
}

func TestLoadConfigAllowsDisablingNoCodexFeatures(t *testing.T) {
	t.Setenv("CODEX_DISABLED_FEATURES", "none")
	t.Setenv("CODEX_RUNNER_TIMEOUT", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(cfg.CodexDisabledFeatures) != 0 {
		t.Fatalf("CodexDisabledFeatures = %v, want empty", cfg.CodexDisabledFeatures)
	}
}

func TestMemorySearchEndpointFromControlAPIAddr(t *testing.T) {
	got := memorySearchEndpoint(":8081")
	want := "http://127.0.0.1:8081/internal/memory/search"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}
