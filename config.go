package main

import (
	"os"
	"strconv"
	"time"
)

const (
	defaultRedisAddr                 = "127.0.0.1:6379"
	defaultStreamPrefix              = "stream:session"
	defaultWeComSeqKey               = "msg:seq"
	defaultEnsureLockPrefix          = "lock:ensure"
	defaultEnsureLockTTLSeconds      = 3
	defaultEnsureRequestTimeoutSecs  = 2
)

type Config struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	StreamPrefix  string
	EnsureLockPrefix string
	EnsureLockTTL    time.Duration

	WeComCorpID     string
	WeComCorpSecret string
	WeComPrivateKey string
	WeComSeqKey     string
	SessionRuntimeEnsureURL string
	EnsureRequestTimeout    time.Duration
}

func LoadConfig() (Config, error) {
	redisDB := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			redisDB = n
		}
	}
	ensureLockTTLSeconds := defaultEnsureLockTTLSeconds
	if v := os.Getenv("ENSURE_LOCK_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ensureLockTTLSeconds = n
		}
	}
	ensureRequestTimeoutSeconds := defaultEnsureRequestTimeoutSecs
	if v := os.Getenv("ENSURE_REQUEST_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ensureRequestTimeoutSeconds = n
		}
	}
	cfg := Config{
		RedisAddr:     envOrDefault("REDIS_ADDR", defaultRedisAddr),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       redisDB,
		StreamPrefix:  envOrDefault("STREAM_PREFIX", defaultStreamPrefix),
		EnsureLockPrefix: envOrDefault("ENSURE_LOCK_PREFIX", defaultEnsureLockPrefix),
		EnsureLockTTL:    time.Duration(ensureLockTTLSeconds) * time.Second,

		WeComCorpID:     os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret: os.Getenv("WECOM_CORP_SECRET"),
		WeComPrivateKey: os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComSeqKey:     envOrDefault("WECOM_SEQ_KEY", defaultWeComSeqKey),
		SessionRuntimeEnsureURL: os.Getenv("SESSION_RUNTIME_ENSURE_URL"),
		EnsureRequestTimeout:    time.Duration(ensureRequestTimeoutSeconds) * time.Second,
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
