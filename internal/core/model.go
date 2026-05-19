package core

import (
	"encoding/json"
	"time"
)

const (
	DefaultTenantID = "default"

	InvocationStatusQueued    int16 = 0
	InvocationStatusRunning   int16 = 1
	InvocationStatusCompleted int16 = 2
	InvocationStatusFailed    int16 = 3
	InvocationStatusCancelled int16 = 4

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
	TriggerPolicy   json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
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

type Invocation struct {
	ID                int64
	RoomID            int64
	Status            int16
	TriggerMessageID  int64
	StartMessageID    int64
	LastSeenMessageID int64
	ErrorDetail       string
	CreatedAt         time.Time
	StartedAt         time.Time
	CompletedAt       time.Time
}

type Delivery struct {
	ID           int64
	RoomID       int64
	InvocationID int64
	Payload      json.RawMessage
	Status       int16
	CreatedAt    time.Time
	AckedAt      time.Time
}

type InboundMessageInput struct {
	Channel         string          `json:"channel"`
	ChannelRoomID   string          `json:"channel_room_id"`
	ChannelRoomType string          `json:"channel_room_type"`
	SourceMessageID string          `json:"source_message_id"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name"`
	MessageTime     time.Time       `json:"message_time"`
	Payload         json.RawMessage `json:"payload"`
	Skipped         bool            `json:"skipped"`
}

type InboundMessageResult struct {
	Room       Room        `json:"room"`
	Message    Message     `json:"message"`
	Invocation *Invocation `json:"invocation,omitempty"`
	Duplicate  bool        `json:"duplicate"`
	Triggered  bool        `json:"triggered"`
	Appended   bool        `json:"appended"`
}

type CompleteInvocationInput struct {
	Text string `json:"text"`
}

type InvocationResult struct {
	Invocation Invocation `json:"invocation"`
	Delivery   *Delivery  `json:"delivery,omitempty"`
}
