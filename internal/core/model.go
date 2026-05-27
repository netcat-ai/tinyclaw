package core

import (
	"encoding/json"
	"time"
)

const (
	DefaultTenantID = "default"

	APIClientPermissionAdapter = "adapter"
	APIClientPermissionAdmin   = "admin"

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
	ID                      int64
	RoomID                  int64
	Enabled                 bool
	TriggerPolicy           json.RawMessage
	PendingTriggerMessageID int64
	CaughtUpMessageID       int64
	CodexSessionID          string
	LockOwner               string
	LockExpiresAt           time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type Message struct {
	ID              int64
	RoomID          int64
	SourceMessageID string
	Source          string
	SenderID        string
	SenderName      string
	Payload         json.RawMessage
	MessageTime     time.Time
	CreatedAt       time.Time
}

type Delivery struct {
	ID                  int64
	RoomID              int64
	AgentSessionID      int64
	SourceMessageFromID int64
	SourceMessageToID   int64
	Payload             json.RawMessage
	Status              int16
	CreatedAt           time.Time
	AckedAt             time.Time
}

type Agent struct {
	ID           int64           `json:"id"`
	Key          string          `json:"key"`
	DisplayName  string          `json:"display_name"`
	Description  string          `json:"description,omitempty"`
	Prompt       string          `json:"prompt"`
	AllowedTools json.RawMessage `json:"allowed_tools"`
	Enabled      bool            `json:"enabled"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type MemoryItem struct {
	ID                    int64     `json:"id"`
	RoomID                int64     `json:"room_id"`
	Type                  string    `json:"type"`
	Key                   string    `json:"key"`
	Content               string    `json:"content"`
	Status                string    `json:"status"`
	SourceMessageFromID   int64     `json:"source_message_from_id"`
	SourceMessageToID     int64     `json:"source_message_to_id"`
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
	Error   string            `json:"error,omitempty"`
}

type MemoryWriteProposal struct {
	Op      string `json:"op"`
	Type    string `json:"type,omitempty"`
	Key     string `json:"key"`
	Content string `json:"content,omitempty"`
}

type MemoryWriteJob struct {
	ID                  int64
	RoomID              int64
	AgentSessionID      int64
	AgentID             int64
	SourceMessageFromID int64
	SourceMessageToID   int64
	OperationKey        string
	Op                  string
	Type                string
	Key                 string
	Content             string
	Status              string
	Attempts            int
	LastError           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type APIClient struct {
	ID          int64
	ClientID    string
	Name        string
	Enabled     bool
	Permissions []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (c APIClient) HasPermission(permission string) bool {
	for _, value := range c.Permissions {
		if value == permission {
			return true
		}
	}
	return false
}

type AdminRoomSummary struct {
	Room                 Room
	AgentSession         AgentSession
	PendingDeliveryCount int64
	LastMessageTime      time.Time
}

type AdminRoomTimeline struct {
	Room          Room
	AgentSessions []AgentSession
	Messages      []Message
	Deliveries    []Delivery
	HasMore       bool
}

type AdminMemoryListInput struct {
	RoomID int64
	Status string
	Types  []string
	Limit  int
}

type UpsertAgentInput struct {
	Key          string          `json:"key"`
	DisplayName  string          `json:"display_name"`
	Description  string          `json:"description"`
	Prompt       string          `json:"prompt"`
	AllowedTools json.RawMessage `json:"allowed_tools"`
	Enabled      bool            `json:"enabled"`
}

type RegisterRoomInput struct {
	Channel         string          `json:"channel"`
	ChannelRoomID   string          `json:"channel_room_id"`
	ChannelRoomType string          `json:"channel_room_type"`
	DisplayName     string          `json:"display_name"`
	OutboundAlias   string          `json:"outbound_alias"`
	AgentEnabled    bool            `json:"agent_enabled"`
	TriggerPolicy   json.RawMessage `json:"trigger_policy"`
}

type RegisterRoomResult struct {
	Room         Room         `json:"room"`
	AgentSession AgentSession `json:"agent_session"`
}

type CreateMessageInput struct {
	RoomID               int64           `json:"room_id"`
	SourceMessageID      string          `json:"source_message_id"`
	Source               string          `json:"source"`
	SenderID             string          `json:"sender_id"`
	SenderName           string          `json:"sender_name"`
	MessageTime          time.Time       `json:"message_time"`
	Payload              json.RawMessage `json:"payload"`
	SuppressAgentTrigger bool            `json:"-"`
}

type CreateMessageResult struct {
	Message   Message `json:"message"`
	Duplicate bool    `json:"duplicate"`
	Triggered bool    `json:"triggered"`
}

type AgentRunResult struct {
	FinalOutput          string                `json:"final_output"`
	MemorySearchRequests []MemorySearchInput   `json:"memory_search_requests,omitempty"`
	MemoryWriteProposals []MemoryWriteProposal `json:"memory_write_proposals,omitempty"`
	CodexSessionID       string                `json:"-"`
}

type AgentRun struct {
	AgentSessionID      int64
	RoomID              int64
	AgentID             int64
	CodexSessionID      string
	SourceMessageFromID int64
	SourceMessageToID   int64
	LockOwner           string
}
