package main

import (
	"fmt"
	"os"
	"strconv"
)

const (
	defaultSandboxNamespace = "claw"
	defaultSandboxTemplate  = "tinyclaw-agent-template"
)

type Config struct {
	DatabaseURL string

	WeComCorpID        string
	WeComCorpSecret    string
	WeComPrivateKey    string
	WeComContactSecret string
	WeComBotID         string

	SandboxNamespace       string
	SandboxTemplateName    string
	SandboxRouterURL       string
	SandboxServerPort      int
	SandboxReadyTimeoutSec int

	WorkToolRobotID string
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

		SandboxNamespace:       sandboxNamespace,
		SandboxTemplateName:    envOrDefault("SANDBOX_TEMPLATE_NAME", defaultSandboxTemplate),
		SandboxRouterURL:       sandboxRouterURL,
		SandboxServerPort:      parseIntEnv("SANDBOX_SERVER_PORT", 8888),
		SandboxReadyTimeoutSec: parseIntEnv("SANDBOX_READY_TIMEOUT_SEC", 180),

		WorkToolRobotID: os.Getenv("WORKTOOL_ROBOT_ID"),
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
