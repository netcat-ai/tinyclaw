package main

import (
	"os"
	"time"
)

type Config struct {
	DatabaseURL string

	ControlAPIAddr  string
	ClawmanAPIToken string

	MetricsAddr string

	AgentRunner        string
	CodexBin           string
	CodexWorkDir       string
	CodexModel         string
	CodexSandbox       string
	CodexRunnerTimeout time.Duration
}

func LoadConfig() (Config, error) {
	timeout, err := time.ParseDuration(envOrDefault("CODEX_RUNNER_TIMEOUT", "5m"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

		ControlAPIAddr:  envOrDefault("CONTROL_API_ADDR", ":8081"),
		ClawmanAPIToken: os.Getenv("CLAWMAN_API_TOKEN"),

		MetricsAddr: envOrDefault("METRICS_ADDR", ":9090"),

		AgentRunner:        os.Getenv("AGENT_RUNNER"),
		CodexBin:           envOrDefault("CODEX_BIN", "codex"),
		CodexWorkDir:       envOrDefault("CODEX_WORKDIR", "."),
		CodexModel:         os.Getenv("CODEX_MODEL"),
		CodexSandbox:       envOrDefault("CODEX_SANDBOX", "workspace-write"),
		CodexRunnerTimeout: timeout,
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
