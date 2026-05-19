package main

import (
	"os"
)

type Config struct {
	DatabaseURL string

	ControlAPIAddr       string
	ClawmanAPIToken      string
	ClawmanInternalToken string

	MetricsAddr string
}

func LoadConfig() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

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
