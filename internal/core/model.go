package core

import (
	"encoding/json"
	"time"
)

const (
	DefaultTenantID = "default"
	DefaultAgentKey = "default"

	DeliveryStatusPending int16 = 0
	DeliveryStatusAcked   int16 = 1
	DeliveryStatusFailed  int16 = 2

	RoomChatTypeDirect = "direct"
	RoomChatTypeGroup  = "group"

	MemoryTypeFact       = "fact"
	MemoryTypePreference = "preference"
	MemoryTypeTodo       = "todo"

	MemoryStatusActive = "active"
	MemoryStatusStale  = "stale"
	MemoryStatusClosed = "closed"

	MemoryWriteOpUpsertFact    = "upsert_fact"
	MemoryWriteOpSetPreference = "set_preference"
	MemoryWriteOpAddTodo       = "add_todo"
	MemoryWriteOpCloseTodo     = "close_todo"
	MemoryWriteOpMarkStale     = "mark_stale"

	MemoryWriteJobStatusPending  = "pending"
	MemoryWriteJobStatusApplied  = "applied"
	MemoryWriteJobStatusFailed   = "failed"
	MemoryWriteJobStatusRejected = "rejected"
)

type Room struct {
	ID              int64
	TenantID        string
	Channel         string
	ChannelRoomID   string
	ChannelRoomType string
	DisplayName     string
	OutboundAlias   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AgentSession struct {
	ID                     int64
	RoomID                 int64
	AgentKey               string
	Enabled                bool
	TriggerPolicy          json.RawMessage
	TriggerMessageID       int64
	LastProcessedMessageID int64
	LockOwner              string
	LockExpiresAt          time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type Message struct {
	ID              int64
	RoomID          int64
	SourceMessageID string
	SenderID        string
	SenderName      string
	Payload         json.RawMessage
	MessageTime     time.Time
	Skipped         bool
	CreatedAt       time.Time
}

type Delivery struct {
	ID                   int64
	RoomID               int64
	AgentSessionID       int64
	SourceMessageAfterID int64
	SourceMessageUntilID int64
	Payload              json.RawMessage
	Status               int16
	CreatedAt            time.Time
	AckedAt              time.Time
}

type MemoryItem struct {
	ID                    int64     `json:"id"`
	RoomID                int64     `json:"room_id"`
	Type                  string    `json:"type"`
	Key                   string    `json:"key"`
	Content               string    `json:"content"`
	Status                string    `json:"status"`
	SourceMessageAfterID  int64     `json:"source_message_after_id"`
	SourceMessageUntilID  int64     `json:"source_message_until_id"`
	CreatedByAgentSession int64     `json:"created_by_agent_session_id"`
	UpdatedByAgentSession int64     `json:"updated_by_agent_session_id"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type MemorySearchInput struct {
	RoomID          int64    `json:"-"`
	Query           string   `json:"query"`
	Types           []string `json:"types"`
	Limit           int      `json:"limit"`
	IncludeInactive bool     `json:"include_inactive"`
}

type MemorySearchResult struct {
	Request MemorySearchInput `json:"request"`
	Items   []MemoryItem      `json:"items"`
}

type MemoryWriteProposal struct {
	Op      string `json:"op"`
	Type    string `json:"type,omitempty"`
	Key     string `json:"key"`
	Content string `json:"content,omitempty"`
}

type MemoryWriteJob struct {
	ID                   int64
	RoomID               int64
	AgentSessionID       int64
	AgentKey             string
	SourceMessageAfterID int64
	SourceMessageUntilID int64
	OperationKey         string
	Op                   string
	Type                 string
	Key                  string
	Content              string
	Status               string
	Attempts             int
	LastError            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type RegisterRoomInput struct {
	Channel         string          `json:"channel"`
	ChannelRoomID   string          `json:"channel_room_id"`
	ChannelRoomType string          `json:"channel_room_type"`
	DisplayName     string          `json:"display_name"`
	OutboundAlias   string          `json:"outbound_alias"`
	AgentKey        string          `json:"agent_key"`
	AgentEnabled    bool            `json:"agent_enabled"`
	TriggerPolicy   json.RawMessage `json:"trigger_policy"`
}

type RegisterRoomResult struct {
	Room         Room         `json:"room"`
	AgentSession AgentSession `json:"agent_session"`
}

type CreateMessageInput struct {
	RoomID          int64           `json:"room_id"`
	SourceMessageID string          `json:"source_message_id"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name"`
	MessageTime     time.Time       `json:"message_time"`
	Payload         json.RawMessage `json:"payload"`
	Skipped         bool            `json:"skipped"`
}

type CreateMessageResult struct {
	Message   Message `json:"message"`
	Duplicate bool    `json:"duplicate"`
	Triggered bool    `json:"triggered"`
}

type AgentRunResult struct {
	FinalOutput           string                `json:"final_output"`
	MemorySearchRequests  []MemorySearchInput   `json:"memory_search_requests,omitempty"`
	MemoryWriteProposals  []MemoryWriteProposal `json:"memory_write_proposals,omitempty"`
}

type AgentRun struct {
	AgentSessionID       int64
	RoomID               int64
	AgentKey             string
	SourceMessageAfterID int64
	SourceMessageUntilID int64
	LockOwner            string
}
