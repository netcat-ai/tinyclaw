package msghub

import (
	"encoding/json"
	"time"
)

const (
	DefaultTenantID = "default"

	ActorKindTerminal = "terminal"
	ActorKindAIAgent  = "aiagent"
	ActorKindAdmin    = "admin"

	EventMessageReceived      = "message.received"
	EventMessageSendRequested = "message.send_requested"
	EventMessageSent          = "message.sent"
	EventMessageFailed        = "message.failed"

	ResourceMessage   = "message"
	ResourceActorTask = "actor_task"

	TaskTypeSendMessage = "send_message"

	TaskStatusPending = "pending"
	TaskStatusAcked   = "acked"
	TaskStatusFailed  = "failed"

	CapabilityReply   = "reply"
	CapabilityAckTask = "ack_task"

	PermissionSubscribeAll = "subscribe_all"
)

type Actor struct {
	ID                int64
	ActorKey          string
	Token             string
	Kind              string
	Name              string
	Enabled           bool
	Channel           string
	ProviderAccountID string
	Permissions       []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (a Actor) HasPermission(permission string) bool {
	for _, value := range a.Permissions {
		if value == permission {
			return true
		}
	}
	return false
}

type Room struct {
	ID                int64
	TenantID          string
	Channel           string
	ProviderAccountID string
	ExternalRoomID    string
	RoomType          string
	DisplayName       string
	OutboundTarget    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Message struct {
	ID                int64
	RoomID            int64
	SourceActorID     int64
	ExternalMessageID string
	SenderID          string
	SenderName        string
	MessageTime       time.Time
	MessageType       string
	Body              json.RawMessage
	CreatedAt         time.Time
}

type ActorTask struct {
	ID               int64
	RoomID           int64
	RequesterActorID int64
	AssigneeActorID  int64
	TaskType         string
	Payload          json.RawMessage
	Status           string
	CreatedAt        time.Time
	AckedAt          time.Time
}

type InboxItem struct {
	ID           int64
	ActorID      int64
	EventType    string
	ResourceType string
	ResourceID   int64
	CreatedAt    time.Time
	AckedAt      time.Time
}

type CapabilityToken struct {
	Token         string
	ActorID       int64
	RoomID        int64
	Action        string
	MessageID     int64
	ActorTaskID   int64
	UsesRemaining int
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type RoomInput struct {
	TenantID          string `json:"tenant_id,omitempty"`
	Channel           string `json:"channel,omitempty"`
	ProviderAccountID string `json:"provider_account_id,omitempty"`
	ExternalRoomID    string `json:"external_room_id,omitempty"`
	RoomType          string `json:"room_type,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	OutboundTarget    string `json:"outbound_target,omitempty"`
}

type SendMessageRequest struct {
	ContextToken      string          `json:"context_token,omitempty"`
	Room              RoomInput       `json:"room,omitempty"`
	ExternalMessageID string          `json:"external_message_id,omitempty"`
	SenderID          string          `json:"sender_id,omitempty"`
	SenderName        string          `json:"sender_name,omitempty"`
	MessageTime       time.Time       `json:"message_time,omitempty"`
	MessageType       string          `json:"message_type,omitempty"`
	Body              json.RawMessage `json:"body,omitempty"`
}

type SendMessageResponse struct {
	Accepted  bool           `json:"accepted"`
	Duplicate bool           `json:"duplicate,omitempty"`
	Message   *MessageView   `json:"message,omitempty"`
	Task      *ActorTaskView `json:"task,omitempty"`
}

type GetUpdatesRequest struct {
	AfterID int64 `json:"after_id,omitempty"`
	Limit   int   `json:"limit,omitempty"`
}

type GetUpdatesResponse struct {
	Updates []UpdateView `json:"updates"`
}

type AckRequest struct {
	UpdateIDs   []int64           `json:"update_ids,omitempty"`
	TaskResults []TaskResultInput `json:"task_results,omitempty"`
}

type TaskResultInput struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type AckResponse struct {
	AckedUpdateIDs []int64         `json:"acked_update_ids"`
	Tasks          []ActorTaskView `json:"tasks"`
}

type UpdateView struct {
	ID           int64           `json:"id"`
	EventType    string          `json:"event_type"`
	ResourceType string          `json:"resource_type"`
	ResourceID   int64           `json:"resource_id"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    time.Time       `json:"created_at"`
	AckedAt      *time.Time      `json:"acked_at,omitempty"`
}

type RoomView struct {
	ID                int64  `json:"id"`
	TenantID          string `json:"tenant_id"`
	Channel           string `json:"channel"`
	ProviderAccountID string `json:"provider_account_id"`
	ExternalRoomID    string `json:"external_room_id"`
	RoomType          string `json:"room_type"`
	DisplayName       string `json:"display_name,omitempty"`
	OutboundTarget    string `json:"outbound_target,omitempty"`
}

type MessageView struct {
	ID                int64           `json:"id"`
	RoomID            int64           `json:"room_id"`
	SourceActorID     int64           `json:"source_actor_id"`
	ExternalMessageID string          `json:"external_message_id"`
	SenderID          string          `json:"sender_id"`
	SenderName        string          `json:"sender_name,omitempty"`
	MessageTime       time.Time       `json:"message_time"`
	MessageType       string          `json:"message_type"`
	Body              json.RawMessage `json:"body"`
	CreatedAt         time.Time       `json:"created_at"`
}

type ActorTaskView struct {
	ID               int64           `json:"id"`
	RoomID           int64           `json:"room_id"`
	RequesterActorID int64           `json:"requester_actor_id,omitempty"`
	AssigneeActorID  int64           `json:"assignee_actor_id"`
	TaskType         string          `json:"task_type"`
	Payload          json.RawMessage `json:"payload"`
	Status           string          `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
	AckedAt          *time.Time      `json:"acked_at,omitempty"`
}

func roomToView(room Room) RoomView {
	return RoomView{
		ID:                room.ID,
		TenantID:          room.TenantID,
		Channel:           room.Channel,
		ProviderAccountID: room.ProviderAccountID,
		ExternalRoomID:    room.ExternalRoomID,
		RoomType:          room.RoomType,
		DisplayName:       room.DisplayName,
		OutboundTarget:    room.OutboundTarget,
	}
}

func messageToView(message Message) MessageView {
	return MessageView{
		ID:                message.ID,
		RoomID:            message.RoomID,
		SourceActorID:     message.SourceActorID,
		ExternalMessageID: message.ExternalMessageID,
		SenderID:          message.SenderID,
		SenderName:        message.SenderName,
		MessageTime:       message.MessageTime,
		MessageType:       message.MessageType,
		Body:              cloneJSON(message.Body),
		CreatedAt:         message.CreatedAt,
	}
}

func taskToView(task ActorTask) ActorTaskView {
	view := ActorTaskView{
		ID:               task.ID,
		RoomID:           task.RoomID,
		RequesterActorID: task.RequesterActorID,
		AssigneeActorID:  task.AssigneeActorID,
		TaskType:         task.TaskType,
		Payload:          cloneJSON(task.Payload),
		Status:           task.Status,
		CreatedAt:        task.CreatedAt,
	}
	if !task.AckedAt.IsZero() {
		ackedAt := task.AckedAt
		view.AckedAt = &ackedAt
	}
	return view
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	out := make([]byte, len(value))
	copy(out, value)
	return json.RawMessage(out)
}
