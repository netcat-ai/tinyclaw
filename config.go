package main

import (
	"os"
	"strconv"
)

const (
	defaultRedisAddr       = "127.0.0.1:6379"
	defaultStreamPrefix    = "stream:group"
	defaultWeComSeqKey     = "msg:seq"
)

type Config struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	StreamPrefix  string

	WeComCorpID      string
	WeComCorpSecret  string
	WeComPrivateKey  string
	WeComSeqKey      string
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
		StreamPrefix:  envOrDefault("STREAM_PREFIX", defaultStreamPrefix),

		WeComCorpID:     os.Getenv("WECOM_CORP_ID"),
		WeComCorpSecret: os.Getenv("WECOM_CORP_SECRET"),
		WeComPrivateKey: os.Getenv("WECOM_RSA_PRIVATE_KEY"),
		WeComSeqKey:     envOrDefault("WECOM_SEQ_KEY", defaultWeComSeqKey),
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
