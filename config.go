package main

import (
	"os"
	"strconv"
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
	CodexBaseURL       string
	CodexAPIKeyEnv     string

	WeComEnabled       bool
	WeComCorpID        string
	WeComCorpSecret    string
	WeComContactSecret string
	WeComRSAPrivateKey string
	WeComBotID         string
	WeComProxy         string
	WeComProxyPassword string
	WeComPollInterval  time.Duration
	WeComPollLimit     int64
	WeComSDKTimeout    int
	WeComStartSeq      int64
}

func LoadConfig() (Config, error) {
	timeout, err := time.ParseDuration(envOrDefault("CODEX_RUNNER_TIMEOUT", "5m"))
	if err != nil {
		return Config{}, err
	}
	wecomPollInterval, err := time.ParseDuration(envOrDefault("WECOM_POLL_INTERVAL", "3s"))
	if err != nil {
		return Config{}, err
	}
	wecomPollLimit, err := parseInt64Env("WECOM_POLL_LIMIT", 100)
	if err != nil {
		return Config{}, err
	}
	wecomSDKTimeout, err := parseIntEnv("WECOM_SDK_TIMEOUT", 30)
	if err != nil {
		return Config{}, err
	}
	wecomStartSeq, err := parseInt64Env("WECOM_START_SEQ", 0)
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
		CodexBaseURL:       os.Getenv("CODEX_BASE_URL"),
		CodexAPIKeyEnv:     envOrDefault("CODEX_API_KEY_ENV", "OPENAI_API_KEY"),

		WeComEnabled:       parseBoolEnv("WECOM_ENABLED"),
		WeComCorpID:        os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret:    os.Getenv("WECOM_CORP_SECRET"),
		WeComContactSecret: os.Getenv("WECOM_CONTACT_SECRET"),
		WeComRSAPrivateKey: os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComBotID:         os.Getenv("WECOM_BOT_ID"),
		WeComProxy:         os.Getenv("WECOM_PROXY"),
		WeComProxyPassword: os.Getenv("WECOM_PROXY_PASSWORD"),
		WeComPollInterval:  wecomPollInterval,
		WeComPollLimit:     wecomPollLimit,
		WeComSDKTimeout:    wecomSDKTimeout,
		WeComStartSeq:      wecomStartSeq,
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

func parseIntEnv(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	return strconv.Atoi(raw)
}

func parseInt64Env(key string, def int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}
