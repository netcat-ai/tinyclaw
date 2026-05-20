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

type AgentRun struct {
	AgentSessionID       int64
	RoomID               int64
	AgentKey             string
	SourceMessageAfterID int64
	SourceMessageUntilID int64
	LockOwner            string
}
