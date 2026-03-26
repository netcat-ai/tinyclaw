package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultSandboxNamespace = "claw"
	defaultSandboxTemplate  = "tinyclaw-agent-template"
)

type Config struct {
	DatabaseURL string

	WeComCorpID               string
	WeComCorpSecret           string
	WeComPrivateKey           string
	WeComContactSecret        string
	WeComBotID                string
	WeComGroupTriggerMentions []string
	WeComGroupTriggerKeywords []string

	SandboxNamespace       string
	SandboxTemplateName    string
	SandboxRouterURL       string
	SandboxServerPort      int
	SandboxReadyTimeoutSec int

	WorkToolRobotID string

	ControlAPIAddr      string
	ControlAPIToken     string
	SendJobLeaseSeconds int

	MetricsAddr string
}

func LoadConfig() (Config, error) {
	sandboxNamespace := envOrDefault("SANDBOX_NAMESPACE", defaultSandboxNamespace)
	sandboxRouterURL := os.Getenv("SANDBOX_ROUTER_URL")
	if sandboxRouterURL == "" {
		sandboxRouterURL = fmt.Sprintf("http://sandbox-router-svc.%s.svc.cluster.local:8080", sandboxNamespace)
	}
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

		WeComCorpID:        os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret:    os.Getenv("WECOM_CORP_SECRET"),
		WeComPrivateKey:    os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComContactSecret: os.Getenv("WECOM_CONTACT_SECRET"),
		WeComBotID:         os.Getenv("WECOM_BOT_ID"),
		WeComGroupTriggerMentions: parseListEnvWithFallback(
			"WECOM_GROUP_TRIGGER_MENTIONS",
			os.Getenv("WECOM_BOT_ID"),
		),
		WeComGroupTriggerKeywords: parseListEnv("WECOM_GROUP_TRIGGER_KEYWORDS"),

		SandboxNamespace:       sandboxNamespace,
		SandboxTemplateName:    envOrDefault("SANDBOX_TEMPLATE_NAME", defaultSandboxTemplate),
		SandboxRouterURL:       sandboxRouterURL,
		SandboxServerPort:      parseIntEnv("SANDBOX_SERVER_PORT", 8888),
		SandboxReadyTimeoutSec: parseIntEnv("SANDBOX_READY_TIMEOUT_SEC", 180),

		WorkToolRobotID: os.Getenv("WORKTOOL_ROBOT_ID"),

		ControlAPIAddr:      envOrDefault("CONTROL_API_ADDR", ":8081"),
		ControlAPIToken:     os.Getenv("CONTROL_API_TOKEN"),
		SendJobLeaseSeconds: parseIntEnv("SEND_JOB_LEASE_SECONDS", 300),

		MetricsAddr: envOrDefault("METRICS_ADDR", ":9090"),
	}

	return cfg, nil
}

func parseIntEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseListEnv(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseListEnvWithFallback(key, fallback string) []string {
	if values := parseListEnv(key); len(values) > 0 {
		return values
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return nil
	}
	return []string{fallback}
}
