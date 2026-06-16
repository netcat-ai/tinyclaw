package main

import (
	"encoding/base64"
	"testing"
)

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
	if !cfg.DrawCommandEnabled {
		t.Fatal("DrawCommandEnabled = false, want true")
	}
	if cfg.ImageProviderBaseURL != "https://code.v4.chat" || cfg.ImageProviderModel != "gpt-image-2" {
		t.Fatalf("image provider defaults = %q/%q", cfg.ImageProviderBaseURL, cfg.ImageProviderModel)
	}
	if cfg.DrawImageSize != "1024x1024" {
		t.Fatalf("DrawImageSize = %q, want 1024x1024", cfg.DrawImageSize)
	}
	if cfg.GeneratedMediaURLTTL.String() != "24h0m0s" {
		t.Fatalf("GeneratedMediaURLTTL = %s, want 24h0m0s", cfg.GeneratedMediaURLTTL)
	}
	if cfg.WOCMediaToken != "" {
		t.Fatalf("WOCMediaToken = %q, want empty when WOC_PASSWORD is unset", cfg.WOCMediaToken)
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

func TestLoadConfigReadsImageProviderKeyFromCodexAuthJSON(t *testing.T) {
	t.Setenv("IMAGE_PROVIDER_API_KEY", "")
	t.Setenv("CODEX_AUTH_JSON", `{"OPENAI_API_KEY":"gateway-key"}`)
	t.Setenv("CODEX_RUNNER_TIMEOUT", "")
	t.Setenv("GENERATED_MEDIA_URL_TTL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.ImageProviderAPIKey != "gateway-key" {
		t.Fatalf("ImageProviderAPIKey = %q, want gateway-key", cfg.ImageProviderAPIKey)
	}
}

func TestLoadConfigBuildsWOCMediaTokenFromBasicCredentials(t *testing.T) {
	t.Setenv("WOC_USERNAME", "agent")
	t.Setenv("WOC_PASSWORD", "secret")
	t.Setenv("CODEX_RUNNER_TIMEOUT", "")
	t.Setenv("GENERATED_MEDIA_URL_TTL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("agent:secret"))
	if cfg.WOCMediaToken != want {
		t.Fatalf("WOCMediaToken = %q, want %q", cfg.WOCMediaToken, want)
	}
}
