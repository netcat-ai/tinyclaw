package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type SessionRuntimeEnsurer interface {
	Ensure(ctx context.Context, event IngressEvent) error
}

type noopSessionRuntimeEnsurer struct{}

func (noopSessionRuntimeEnsurer) Ensure(context.Context, IngressEvent) error {
	return nil
}

type ensureLocker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

type redisEnsureLocker struct {
	redis *redis.Client
}

func (l redisEnsureLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	acquired, err := l.redis.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, err
	}
	return acquired, nil
}

type ensureHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type httpSessionRuntimeEnsurer struct {
	url        string
	lockPrefix string
	lockTTL    time.Duration
	locker     ensureLocker
	httpClient ensureHTTPClient
}

type ensureRequest struct {
	SessionKey string `json:"session_key"`
	TenantID   string `json:"tenant_id"`
	ChatType   string `json:"chat_type"`
	TraceID    string `json:"trace_id"`
}

func newSessionRuntimeEnsurer(cfg Config, redisClient *redis.Client) SessionRuntimeEnsurer {
	if cfg.SessionRuntimeEnsureURL == "" || redisClient == nil {
		return noopSessionRuntimeEnsurer{}
	}

	return &httpSessionRuntimeEnsurer{
		url:        cfg.SessionRuntimeEnsureURL,
		lockPrefix: cfg.EnsureLockPrefix,
		lockTTL:    cfg.EnsureLockTTL,
		locker:     redisEnsureLocker{redis: redisClient},
		httpClient: &http.Client{Timeout: cfg.EnsureRequestTimeout},
	}
}

func (e *httpSessionRuntimeEnsurer) Ensure(ctx context.Context, event IngressEvent) error {
	if event.SessionKey == "" {
		return fmt.Errorf("session key is empty")
	}

	locked, err := e.locker.Acquire(ctx, ensureLockKey(e.lockPrefix, event.SessionKey), e.lockTTL)
	if err != nil {
		return fmt.Errorf("acquire ensure lock: %w", err)
	}
	if !locked {
		return nil
	}

	payload := ensureRequest{
		SessionKey: event.SessionKey,
		TenantID:   event.TenantID,
		ChatType:   event.ChatType,
		TraceID:    event.TraceID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ensure request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ensure request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send ensure request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ensure request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return nil
}

func ensureLockKey(prefix, sessionKey string) string {
	return prefix + ":" + sessionKey
}
