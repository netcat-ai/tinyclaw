package main

import (
	"os"
	"strconv"
)

const (
	defaultRedisAddr    = "127.0.0.1:6379"
	defaultStreamPrefix = "stream:room"
	defaultWeComSeqKey  = "msg:seq"
)

type Config struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	StreamPrefix  string

	WeComCorpID        string
	WeComCorpSecret    string
	WeComPrivateKey    string
	WeComSeqKey        string
	WeComContactSecret string
	WeComBotID         string

	SandboxEnabled   bool
	SandboxNamespace string
	SandboxImage     string
}

func LoadConfig() (Config, error) {
	redisDB := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			redisDB = n
		}
	}
	sandboxEnabled := false
	if v := os.Getenv("SANDBOX_ENABLED"); v != "" {
		sandboxEnabled, _ = strconv.ParseBool(v)
	}

	cfg := Config{
		RedisAddr:     envOrDefault("REDIS_ADDR", defaultRedisAddr),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       redisDB,
		StreamPrefix:  envOrDefault("STREAM_PREFIX", defaultStreamPrefix),

		WeComCorpID:        os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret:    os.Getenv("WECOM_CORP_SECRET"),
		WeComPrivateKey:    os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComSeqKey:        envOrDefault("WECOM_SEQ_KEY", defaultWeComSeqKey),
		WeComContactSecret: os.Getenv("WECOM_CONTACT_SECRET"),
		WeComBotID:         os.Getenv("WECOM_BOT_ID"),

		SandboxEnabled:   sandboxEnabled,
		SandboxNamespace: envOrDefault("SANDBOX_NAMESPACE", "claw"),
		SandboxImage:     os.Getenv("SANDBOX_IMAGE"),
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
