package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// SpawnRequest 定义子会话生成请求
type SpawnRequest struct {
	ParentSessionKey string                 `json:"parent_session_key"`
	AgentID          string                 `json:"agent_id"`
	Task             string                 `json:"task"`
	Model            string                 `json:"model,omitempty"`
	Context          map[string]interface{} `json:"context,omitempty"`
	TenantID         string                 `json:"tenant_id"`
	ChatType         string                 `json:"chat_type"`
}

// SpawnResponse 定义子会话生成响应
type SpawnResponse struct {
	ChildSessionKey string `json:"child_session_key"`
	AgentID         string `json:"agent_id"`
	StreamKey       string `json:"stream_key"`
}

// AnnounceRequest 子agent完成后回传结果
type AnnounceRequest struct {
	ParentSessionKey string `json:"parent_session_key"`
	ChildSessionKey  string `json:"child_session_key"`
	AgentID          string `json:"agent_id"`
	Result           string `json:"result"`
	Status           string `json:"status"`
}

// SessionSpawner 负责生成子会话
type SessionSpawner struct {
	redis        *redis.Client
	ensurer      SessionRuntimeEnsurer
	streamPrefix string
}

func NewSessionSpawner(redis *redis.Client, ensurer SessionRuntimeEnsurer, streamPrefix string) *SessionSpawner {
	return &SessionSpawner{
		redis:        redis,
		ensurer:      ensurer,
		streamPrefix: streamPrefix,
	}
}

// Spawn 创建子会话并返回子会话key
func (s *SessionSpawner) Spawn(ctx context.Context, req SpawnRequest) (*SpawnResponse, error) {
	if req.ParentSessionKey == "" {
		return nil, fmt.Errorf("parent_session_key is required")
	}
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if req.Task == "" {
		return nil, fmt.Errorf("task is required")
	}

	childSessionKey := childSessionKey(req.ParentSessionKey)
	spawnID := newID()

	event := IngressEvent{
		EventID:     "spawn:" + spawnID,
		EventType:   eventTypeSubagentTask,
		TenantID:    req.TenantID,
		SessionKey:  childSessionKey,
		SenderID:    "system",
		ChatType:    req.ChatType,
		ContentType: "text",
		Content:     req.Task,
		Attachments: "[]",
		OccurredAt:  time.Now().UTC(),
		TraceID:     "spawn:" + childSessionKey,
	}

	streamKey := s.streamPrefix + ":" + childSessionKey
	if err := s.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: streamValues(event),
	}).Err(); err != nil {
		return nil, fmt.Errorf("xadd child stream: %w", err)
	}

	ensureEvent := IngressEvent{
		SessionKey: childSessionKey,
		TenantID:   req.TenantID,
		ChatType:   req.ChatType,
		TraceID:    event.TraceID,
	}
	if err := s.ensurer.Ensure(ctx, ensureEvent); err != nil {
		return nil, fmt.Errorf("ensure child session: %w", err)
	}

	return &SpawnResponse{
		ChildSessionKey: childSessionKey,
		AgentID:         req.AgentID,
		StreamKey:       streamKey,
	}, nil
}

// Announce 子agent完成后将结果写入父stream
func (s *SessionSpawner) Announce(ctx context.Context, req AnnounceRequest) error {
	if req.ParentSessionKey == "" {
		return fmt.Errorf("parent_session_key is required")
	}
	if req.Result == "" {
		return fmt.Errorf("result is required")
	}

	status := req.Status
	if status == "" {
		status = "completed"
	}

	event := IngressEvent{
		EventID:     "announce:" + newID(),
		EventType:   eventTypeSubagentResult,
		SessionKey:  req.ParentSessionKey,
		SenderID:    req.AgentID,
		ContentType: "text",
		Content:     req.Result,
		Attachments: "[]",
		OccurredAt:  time.Now().UTC(),
		TraceID:     "announce:" + req.ChildSessionKey,
		Raw:         fmt.Sprintf(`{"child_session_key":%q,"agent_id":%q,"status":%q}`, req.ChildSessionKey, req.AgentID, status),
	}

	stream := s.streamPrefix + ":" + req.ParentSessionKey
	if err := s.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: streamValues(event),
	}).Err(); err != nil {
		return fmt.Errorf("xadd parent stream: %w", err)
	}

	return nil
}

func childSessionKey(parentKey string) string {
	return parentKey + ":subagent:" + newID()
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
