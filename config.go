package main

import (
	"os"
	"strings"
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

	ControlAPIAddr       string
	ClawmanAPIToken      string
	ClawmanInternalToken string

	MetricsAddr string
}

func LoadConfig() (Config, error) {
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

		ControlAPIAddr:       envOrDefault("CONTROL_API_ADDR", ":8081"),
		ClawmanAPIToken:      os.Getenv("CLAWMAN_API_TOKEN"),
		ClawmanInternalToken: os.Getenv("CLAWMAN_INTERNAL_TOKEN"),

		MetricsAddr: envOrDefault("METRICS_ADDR", ":9090"),
	}

	return cfg, nil
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
