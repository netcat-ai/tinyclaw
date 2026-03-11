package main

import (
	"os"
	"strconv"
)

const (
	defaultRedisAddr   = "127.0.0.1:6379"
	defaultWeComSeqKey = "msg:seq"
)

type Config struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	WeComCorpID        string
	WeComCorpSecret    string
	WeComPrivateKey    string
	WeComSeqKey        string
	WeComContactSecret string
	WeComBotID         string

	SandboxNamespace string
	SandboxImage     string

	WorkToolRobotID string

	ModelAPIBaseURL string
	ModelAPIKey     string
}

func LoadConfig() (Config, error) {
	redisDB := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			redisDB = n
		}
	}
	cfg := Config{
		RedisAddr:     envOrDefault("REDIS_ADDR", defaultRedisAddr),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       redisDB,

		WeComCorpID:        os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret:    os.Getenv("WECOM_CORP_SECRET"),
		WeComPrivateKey:    os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComSeqKey:        envOrDefault("WECOM_SEQ_KEY", defaultWeComSeqKey),
		WeComContactSecret: os.Getenv("WECOM_CONTACT_SECRET"),
		WeComBotID:         os.Getenv("WECOM_BOT_ID"),

		SandboxNamespace: "claw",
		SandboxImage:     os.Getenv("SANDBOX_IMAGE"),

		WorkToolRobotID: os.Getenv("WORKTOOL_ROBOT_ID"),

		ModelAPIBaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
		ModelAPIKey:     os.Getenv("ANTHROPIC_API_KEY"),
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
