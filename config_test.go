package main

import "testing"

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("TEST_ENV_OR_DEFAULT", "value")
	if got := envOrDefault("TEST_ENV_OR_DEFAULT", "fallback"); got != "value" {
		t.Fatalf("envOrDefault() = %q, want %q", got, "value")
	}

	if got := envOrDefault("TEST_ENV_OR_DEFAULT_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault() = %q, want %q", got, "fallback")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("STREAM_PREFIX", "")
	t.Setenv("WECOM_CORP_ID", "")
	t.Setenv("WECOM_CORP_SECRET", "")
	t.Setenv("WECOM_RSA_PRIVATE_KEY", "")
	t.Setenv("WECOM_SEQ_KEY", "")
	t.Setenv("ENSURE_LOCK_PREFIX", "")
	t.Setenv("ENSURE_LOCK_TTL_SECONDS", "")
	t.Setenv("SESSION_RUNTIME_ENSURE_URL", "")
	t.Setenv("ENSURE_REQUEST_TIMEOUT_SECONDS", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.RedisAddr != defaultRedisAddr {
		t.Fatalf("RedisAddr = %q, want %q", cfg.RedisAddr, defaultRedisAddr)
	}
	if cfg.RedisDB != 0 {
		t.Fatalf("RedisDB = %d, want 0", cfg.RedisDB)
	}
	if cfg.StreamPrefix != defaultStreamPrefix {
		t.Fatalf("StreamPrefix = %q, want %q", cfg.StreamPrefix, defaultStreamPrefix)
	}
	if cfg.WeComSeqKey != defaultWeComSeqKey {
		t.Fatalf("WeComSeqKey = %q, want %q", cfg.WeComSeqKey, defaultWeComSeqKey)
	}
	if cfg.EnsureLockPrefix != defaultEnsureLockPrefix {
		t.Fatalf("EnsureLockPrefix = %q, want %q", cfg.EnsureLockPrefix, defaultEnsureLockPrefix)
	}
	if cfg.EnsureLockTTL.Seconds() != defaultEnsureLockTTLSeconds {
		t.Fatalf("EnsureLockTTL = %v, want %ds", cfg.EnsureLockTTL, defaultEnsureLockTTLSeconds)
	}
	if cfg.EnsureRequestTimeout.Seconds() != defaultEnsureRequestTimeoutSecs {
		t.Fatalf("EnsureRequestTimeout = %v, want %ds", cfg.EnsureRequestTimeout, defaultEnsureRequestTimeoutSecs)
	}
	if cfg.SessionRuntimeEnsureURL != "" {
		t.Fatalf("SessionRuntimeEnsureURL = %q, want empty", cfg.SessionRuntimeEnsureURL)
	}
	if cfg.RedisPassword != "" {
		t.Fatalf("RedisPassword = %q, want empty", cfg.RedisPassword)
	}
}

func TestLoadConfigReadsEnv(t *testing.T) {
	t.Setenv("REDIS_ADDR", "10.0.0.9:6379")
	t.Setenv("REDIS_PASSWORD", "secret")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("STREAM_PREFIX", "stream:session")
	t.Setenv("WECOM_CORP_ID", "corp")
	t.Setenv("WECOM_CORP_SECRET", "corp-secret")
	t.Setenv("WECOM_RSA_PRIVATE_KEY", "private-key")
	t.Setenv("WECOM_SEQ_KEY", "msg:seq:test")
	t.Setenv("ENSURE_LOCK_PREFIX", "lock:ensure")
	t.Setenv("ENSURE_LOCK_TTL_SECONDS", "5")
	t.Setenv("SESSION_RUNTIME_ENSURE_URL", "http://127.0.0.1:18080/internal/session-runtime/ensure")
	t.Setenv("ENSURE_REQUEST_TIMEOUT_SECONDS", "4")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.RedisAddr != "10.0.0.9:6379" {
		t.Fatalf("RedisAddr = %q, want %q", cfg.RedisAddr, "10.0.0.9:6379")
	}
	if cfg.RedisPassword != "secret" {
		t.Fatalf("RedisPassword = %q, want %q", cfg.RedisPassword, "secret")
	}
	if cfg.RedisDB != 3 {
		t.Fatalf("RedisDB = %d, want 3", cfg.RedisDB)
	}
	if cfg.StreamPrefix != "stream:session" {
		t.Fatalf("StreamPrefix = %q, want %q", cfg.StreamPrefix, "stream:session")
	}
	if cfg.WeComCorpID != "corp" {
		t.Fatalf("WeComCorpID = %q, want %q", cfg.WeComCorpID, "corp")
	}
	if cfg.WeComCorpSecret != "corp-secret" {
		t.Fatalf("WeComCorpSecret = %q, want %q", cfg.WeComCorpSecret, "corp-secret")
	}
	if cfg.WeComPrivateKey != "private-key" {
		t.Fatalf("WeComPrivateKey = %q, want %q", cfg.WeComPrivateKey, "private-key")
	}
	if cfg.WeComSeqKey != "msg:seq:test" {
		t.Fatalf("WeComSeqKey = %q, want %q", cfg.WeComSeqKey, "msg:seq:test")
	}
	if cfg.EnsureLockPrefix != "lock:ensure" {
		t.Fatalf("EnsureLockPrefix = %q, want %q", cfg.EnsureLockPrefix, "lock:ensure")
	}
	if cfg.EnsureLockTTL.Seconds() != 5 {
		t.Fatalf("EnsureLockTTL = %v, want 5s", cfg.EnsureLockTTL)
	}
	if cfg.SessionRuntimeEnsureURL != "http://127.0.0.1:18080/internal/session-runtime/ensure" {
		t.Fatalf("SessionRuntimeEnsureURL = %q, want %q", cfg.SessionRuntimeEnsureURL, "http://127.0.0.1:18080/internal/session-runtime/ensure")
	}
	if cfg.EnsureRequestTimeout.Seconds() != 4 {
		t.Fatalf("EnsureRequestTimeout = %v, want 4s", cfg.EnsureRequestTimeout)
	}
}
