package main

import (
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string

	ControlAPIAddr     string
	ClawmanAPIToken    string
	ClawmanAdminSecret string
	WOCPanelBaseURL    string
	WOCUsername        string
	WOCPassword        string
	WOCMediaToken      string

	MetricsAddr string

	AgentRunner            string
	AgentWorkerConcurrency int
	CodexBin               string
	CodexWorkDir           string
	CodexModel             string
	CodexSandbox           string
	CodexOpenAIBaseURL     string
	CodexDisabledFeatures  []string
	CodexRunnerTimeout     time.Duration

	GeneratedMediaS3Endpoint        string
	GeneratedMediaS3Bucket          string
	GeneratedMediaS3Region          string
	GeneratedMediaS3AccessKeyID     string
	GeneratedMediaS3SecretAccessKey string
	GeneratedMediaS3ForcePathStyle  bool
	GeneratedMediaURLTTL            time.Duration
}

func LoadConfig() (Config, error) {
	timeout, err := time.ParseDuration(envOrDefault("CODEX_RUNNER_TIMEOUT", "5m"))
	if err != nil {
		return Config{}, err
	}
	generatedMediaURLTTL, err := time.ParseDuration(envOrDefault("GENERATED_MEDIA_URL_TTL", "24h"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

		ControlAPIAddr:     envOrDefault("CONTROL_API_ADDR", ":8081"),
		ClawmanAPIToken:    os.Getenv("CLAWMAN_API_TOKEN"),
		ClawmanAdminSecret: os.Getenv("CLAWMAN_ADMIN_SECRET"),
		WOCPanelBaseURL:    envOrDefault("WOC_PANEL_BASE_URL", "http://127.0.0.1:36080"),
		WOCUsername:        envOrDefault("WOC_USERNAME", "agent"),
		WOCPassword:        os.Getenv("WOC_PASSWORD"),

		MetricsAddr: envOrDefault("METRICS_ADDR", ":9090"),

		AgentRunner:            os.Getenv("AGENT_RUNNER"),
		AgentWorkerConcurrency: parsePositiveIntEnvDefault("AGENT_WORKER_CONCURRENCY", 2),
		CodexBin:               envOrDefault("CODEX_BIN", "codex"),
		CodexWorkDir:           envOrDefault("CODEX_WORKDIR", "."),
		CodexModel:             os.Getenv("CODEX_MODEL"),
		CodexSandbox:           envOrDefault("CODEX_SANDBOX", "workspace-write"),
		CodexOpenAIBaseURL: strings.TrimSpace(envOrDefault(
			"CODEX_OPENAI_BASE_URL",
			os.Getenv("OPENAI_BASE_URL"),
		)),
		CodexDisabledFeatures: parseCSVEnv("CODEX_DISABLED_FEATURES", []string{
			"apps",
			"tool_suggest",
			"plugins",
		}),
		CodexRunnerTimeout: timeout,

		GeneratedMediaS3Endpoint:        os.Getenv("GENERATED_MEDIA_S3_ENDPOINT"),
		GeneratedMediaS3Bucket:          os.Getenv("GENERATED_MEDIA_S3_BUCKET"),
		GeneratedMediaS3Region:          os.Getenv("GENERATED_MEDIA_S3_REGION"),
		GeneratedMediaS3AccessKeyID:     os.Getenv("GENERATED_MEDIA_S3_ACCESS_KEY_ID"),
		GeneratedMediaS3SecretAccessKey: os.Getenv("GENERATED_MEDIA_S3_SECRET_ACCESS_KEY"),
		GeneratedMediaS3ForcePathStyle:  parseBoolEnv("GENERATED_MEDIA_S3_FORCE_PATH_STYLE"),
		GeneratedMediaURLTTL:            generatedMediaURLTTL,
	}
	cfg.WOCMediaToken = wocBasicQueryToken(cfg.WOCUsername, cfg.WOCPassword)

	return cfg, nil
}

func wocBasicQueryToken(username string, password string) string {
	if strings.TrimSpace(password) == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(username) + ":" + password))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBoolEnv(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && v
}

func parseBoolEnvDefault(key string, def bool) bool {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func parsePositiveIntEnvDefault(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func parseCSVEnv(key string, def []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return append([]string(nil), def...)
	}
	if strings.EqualFold(strings.TrimSpace(raw), "none") {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}
