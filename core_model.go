package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultTenantID = "default"

	dispatchWaiting int64 = 0
	dispatchSkipped int64 = 1

	invocationStatusQueued    = "queued"
	invocationStatusRunning   = "running"
	invocationStatusCompleted = "completed"
	invocationStatusFailed    = "failed"

	deliveryStatusPending = "pending"
	deliveryStatusAcked   = "acked"
)

type CoreRoom struct {
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

type CoreMessage struct {
	ID              int64
	RoomID          int64
	SourceMessageID string
	SenderID        string
	SenderName      string
	Payload         json.RawMessage
	MessageTime     time.Time
	DispatchState   int64
	CreatedAt       time.Time
}

type CoreInvocation struct {
	ID               int64
	RoomID           int64
	Status           string
	TriggerMessageID int64
	InputSnapshot    json.RawMessage
	OutputSnapshot   json.RawMessage
	CreatedAt        time.Time
	StartedAt        time.Time
	CompletedAt      time.Time
}

type CoreDelivery struct {
	ID           int64
	Seq          int64
	RoomID       int64
	InvocationID int64
	Payload      json.RawMessage
	Status       string
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
	Room        CoreRoom        `json:"room"`
	Message     CoreMessage     `json:"message"`
	Invocation  *CoreInvocation `json:"invocation,omitempty"`
	Duplicate   bool            `json:"duplicate"`
	Triggered   bool            `json:"triggered"`
	Appended    bool            `json:"appended"`
	DispatchSet int64           `json:"dispatch_state"`
}

type CompleteInvocationInput struct {
	Output json.RawMessage `json:"output_snapshot"`
	Text   string          `json:"text"`
}

type InvocationResult struct {
	Invocation CoreInvocation `json:"invocation"`
	Delivery   *CoreDelivery  `json:"delivery,omitempty"`
}

func (s *Store) IngestCoreMessage(ctx context.Context, input InboundMessageInput) (InboundMessageResult, error) {
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChannelRoomID = strings.TrimSpace(input.ChannelRoomID)
	input.ChannelRoomType = strings.TrimSpace(input.ChannelRoomType)
	input.SourceMessageID = strings.TrimSpace(input.SourceMessageID)
	input.SenderID = strings.TrimSpace(input.SenderID)
	input.SenderName = strings.TrimSpace(input.SenderName)
	if input.MessageTime.IsZero() {
		input.MessageTime = time.Now().UTC()
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	if err := validateInboundMessage(input); err != nil {
		return InboundMessageResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InboundMessageResult{}, fmt.Errorf("begin core inbound tx: %w", err)
	}
	defer tx.Rollback()

	room, err := upsertCoreRoomTx(ctx, tx, input)
	if err != nil {
		return InboundMessageResult{}, err
	}

	inserted, message, err := insertCoreMessageTx(ctx, tx, room.ID, input)
	if err != nil {
		return InboundMessageResult{}, err
	}
	result := InboundMessageResult{
		Room:        room,
		Message:     message,
		Duplicate:   !inserted,
		DispatchSet: message.DispatchState,
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return InboundMessageResult{}, fmt.Errorf("commit duplicate core inbound: %w", err)
		}
		return result, nil
	}

	if input.Skipped {
		message, err = updateCoreMessageDispatchStateTx(ctx, tx, message.ID, dispatchSkipped)
		if err != nil {
			return InboundMessageResult{}, err
		}
		result.Message = message
		result.DispatchSet = message.DispatchState
		if err := tx.Commit(); err != nil {
			return InboundMessageResult{}, fmt.Errorf("commit skipped core inbound: %w", err)
		}
		return result, nil
	}

	active, hasActive, err := getActiveInvocationForRoomTx(ctx, tx, room.ID)
	if err != nil {
		return InboundMessageResult{}, err
	}
	if hasActive {
		message, err = updateCoreMessageDispatchStateTx(ctx, tx, message.ID, active.ID)
		if err != nil {
			return InboundMessageResult{}, err
		}
		result.Message = message
		result.Invocation = &active
		result.Appended = true
		result.DispatchSet = message.DispatchState
		if err := tx.Commit(); err != nil {
			return InboundMessageResult{}, fmt.Errorf("commit appended core inbound: %w", err)
		}
		return result, nil
	}

	if shouldTriggerCoreMessage(room, input) {
		invocation, err := createInvocationFromWaitingMessagesTx(ctx, tx, room.ID, message.ID)
		if err != nil {
			return InboundMessageResult{}, err
		}
		message, err = getCoreMessageByIDTx(ctx, tx, message.ID)
		if err != nil {
			return InboundMessageResult{}, err
		}
		result.Message = message
		result.Invocation = &invocation
		result.Triggered = true
		result.DispatchSet = message.DispatchState
	}

	if err := tx.Commit(); err != nil {
		return InboundMessageResult{}, fmt.Errorf("commit core inbound: %w", err)
	}
	return result, nil
}

func (s *Store) CompleteCoreInvocation(ctx context.Context, invocationID int64, input CompleteInvocationInput) (InvocationResult, error) {
	output := input.Output
	if len(output) == 0 {
		output = mustJSON(map[string]any{
			"status":       "completed",
			"final_output": input.Text,
		})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("begin complete invocation tx: %w", err)
	}
	defer tx.Rollback()

	invocation, err := updateInvocationTerminalTx(ctx, tx, invocationID, invocationStatusCompleted, output)
	if err != nil {
		return InvocationResult{}, err
	}
	var delivery *CoreDelivery
	if strings.TrimSpace(input.Text) != "" {
		created, err := createCoreDeliveryTx(ctx, tx, invocation.RoomID, invocation.ID, mustJSON(map[string]any{
			"type": "text",
			"text": input.Text,
		}))
		if err != nil {
			return InvocationResult{}, err
		}
		delivery = &created
	}
	if err := tx.Commit(); err != nil {
		return InvocationResult{}, fmt.Errorf("commit complete invocation: %w", err)
	}
	return InvocationResult{Invocation: invocation, Delivery: delivery}, nil
}

func (s *Store) FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (InvocationResult, error) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "执行失败，请稍后重试"
	}
	output := mustJSON(map[string]any{
		"status":       "failed",
		"final_output": detail,
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("begin fail invocation tx: %w", err)
	}
	defer tx.Rollback()

	invocation, err := updateInvocationTerminalTx(ctx, tx, invocationID, invocationStatusFailed, output)
	if err != nil {
		return InvocationResult{}, err
	}
	delivery, err := createCoreDeliveryTx(ctx, tx, invocation.RoomID, invocation.ID, mustJSON(map[string]any{
		"type": "text",
		"text": detail,
	}))
	if err != nil {
		return InvocationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InvocationResult{}, fmt.Errorf("commit fail invocation: %w", err)
	}
	return InvocationResult{Invocation: invocation, Delivery: &delivery}, nil
}

func (s *Store) ListCoreDeliveries(ctx context.Context, channel string, afterSeq int64) ([]CoreDelivery, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.seq, d.room_id, d.invocation_id, d.payload, d.status, d.created_at, d.acked_at
		FROM core_deliveries d
		JOIN core_rooms r ON r.id = d.room_id
		WHERE r.channel = $1
		  AND d.seq > $2
		  AND d.status = $3
		ORDER BY d.seq ASC
	`, channel, afterSeq, deliveryStatusPending)
	if err != nil {
		return nil, fmt.Errorf("list core deliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []CoreDelivery
	for rows.Next() {
		delivery, err := scanCoreDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate core deliveries: %w", err)
	}
	return deliveries, nil
}

func (s *Store) AckCoreDelivery(ctx context.Context, id int64) (CoreDelivery, error) {
	return ackCoreDelivery(ctx, s.db, id)
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
