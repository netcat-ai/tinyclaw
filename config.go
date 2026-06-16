package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string

	ControlAPIAddr     string
	ClawmanAPIToken    string
	ClawmanAdminSecret string

	MetricsAddr string

	AgentRunner           string
	CodexBin              string
	CodexWorkDir          string
	CodexModel            string
	CodexSandbox          string
	CodexDisabledFeatures []string
	CodexRunnerTimeout    time.Duration

	DrawCommandEnabled   bool
	ImageProviderBaseURL string
	ImageProviderModel   string
	ImageProviderAPIKey  string
	DrawImageSize        string

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
	imageProviderAPIKey := os.Getenv("IMAGE_PROVIDER_API_KEY")
	if strings.TrimSpace(imageProviderAPIKey) == "" {
		imageProviderAPIKey = codexAuthOpenAIAPIKey()
	}
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

		ControlAPIAddr:     envOrDefault("CONTROL_API_ADDR", ":8081"),
		ClawmanAPIToken:    os.Getenv("CLAWMAN_API_TOKEN"),
		ClawmanAdminSecret: os.Getenv("CLAWMAN_ADMIN_SECRET"),

		MetricsAddr: envOrDefault("METRICS_ADDR", ":9090"),

		AgentRunner:  os.Getenv("AGENT_RUNNER"),
		CodexBin:     envOrDefault("CODEX_BIN", "codex"),
		CodexWorkDir: envOrDefault("CODEX_WORKDIR", "."),
		CodexModel:   os.Getenv("CODEX_MODEL"),
		CodexSandbox: envOrDefault("CODEX_SANDBOX", "workspace-write"),
		CodexDisabledFeatures: parseCSVEnv("CODEX_DISABLED_FEATURES", []string{
			"apps",
			"tool_suggest",
			"plugins",
		}),
		CodexRunnerTimeout: timeout,

		DrawCommandEnabled:   parseBoolEnvDefault("DRAW_COMMAND_ENABLED", true),
		ImageProviderBaseURL: envOrDefault("IMAGE_PROVIDER_BASE_URL", "https://code.v4.chat"),
		ImageProviderModel:   envOrDefault("IMAGE_PROVIDER_MODEL", "gpt-image-2"),
		ImageProviderAPIKey:  imageProviderAPIKey,
		DrawImageSize:        envOrDefault("DRAW_IMAGE_SIZE", "1024x1024"),

		GeneratedMediaS3Endpoint:        os.Getenv("GENERATED_MEDIA_S3_ENDPOINT"),
		GeneratedMediaS3Bucket:          os.Getenv("GENERATED_MEDIA_S3_BUCKET"),
		GeneratedMediaS3Region:          os.Getenv("GENERATED_MEDIA_S3_REGION"),
		GeneratedMediaS3AccessKeyID:     os.Getenv("GENERATED_MEDIA_S3_ACCESS_KEY_ID"),
		GeneratedMediaS3SecretAccessKey: os.Getenv("GENERATED_MEDIA_S3_SECRET_ACCESS_KEY"),
		GeneratedMediaS3ForcePathStyle:  parseBoolEnv("GENERATED_MEDIA_S3_FORCE_PATH_STYLE"),
		GeneratedMediaURLTTL:            generatedMediaURLTTL,
	}

	return cfg, nil
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

func codexAuthOpenAIAPIKey() string {
	for _, raw := range []string{os.Getenv("CODEX_AUTH_JSON"), readCodexAuthFile()} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var parsed struct {
			OpenAIAPIKey string `json:"OPENAI_API_KEY"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && strings.TrimSpace(parsed.OpenAIAPIKey) != "" {
			return strings.TrimSpace(parsed.OpenAIAPIKey)
		}
	}
	return ""
}

func readCodexAuthFile() string {
	path := strings.TrimSpace(os.Getenv("CODEX_AUTH_PATH"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".codex", "auth.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
