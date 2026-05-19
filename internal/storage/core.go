package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

type CoreStore struct {
	db *sql.DB
}

func NewCoreStore(db *sql.DB) *CoreStore {
	return &CoreStore{db: db}
}

func (s *CoreStore) IngestCoreMessage(ctx context.Context, input core.InboundMessageInput) (core.InboundMessageResult, error) {
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
		return core.InboundMessageResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.InboundMessageResult{}, fmt.Errorf("begin core inbound tx: %w", err)
	}
	defer tx.Rollback()

	room, err := upsertCoreRoomTx(ctx, tx, input)
	if err != nil {
		return core.InboundMessageResult{}, err
	}

	inserted, message, err := insertCoreMessageTx(ctx, tx, room.ID, input)
	if err != nil {
		return core.InboundMessageResult{}, err
	}
	result := core.InboundMessageResult{
		Room:      room,
		Message:   message,
		Duplicate: !inserted,
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return core.InboundMessageResult{}, fmt.Errorf("commit duplicate core inbound: %w", err)
		}
		return result, nil
	}

	if input.Skipped {
		if err := tx.Commit(); err != nil {
			return core.InboundMessageResult{}, fmt.Errorf("commit skipped core inbound: %w", err)
		}
		return result, nil
	}

	active, hasActive, err := getActiveInvocationForRoomTx(ctx, tx, room.ID)
	if err != nil {
		return core.InboundMessageResult{}, err
	}
	if hasActive {
		result.Invocation = &active
		result.Appended = true
		if err := tx.Commit(); err != nil {
			return core.InboundMessageResult{}, fmt.Errorf("commit appended core inbound: %w", err)
		}
		return result, nil
	}

	if core.ShouldTriggerMessage(room, input) {
		invocation, err := createCoreInvocationTx(ctx, tx, room.ID, message.ID)
		if err != nil {
			return core.InboundMessageResult{}, err
		}
		result.Invocation = &invocation
		result.Triggered = true
	}

	if err := tx.Commit(); err != nil {
		return core.InboundMessageResult{}, fmt.Errorf("commit core inbound: %w", err)
	}
	return result, nil
}

func (s *CoreStore) CompleteCoreInvocation(ctx context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.InvocationResult{}, fmt.Errorf("begin complete invocation tx: %w", err)
	}
	defer tx.Rollback()

	invocation, err := updateInvocationCompletedTx(ctx, tx, invocationID)
	if err != nil {
		return core.InvocationResult{}, err
	}
	room, err := getCoreRoomByIDTx(ctx, tx, invocation.RoomID)
	if err != nil {
		return core.InvocationResult{}, err
	}
	var delivery *core.Delivery
	if strings.TrimSpace(input.Text) != "" {
		created, err := createCoreDeliveryTx(ctx, tx, invocation.RoomID, invocation.ID, deliveryTextPayload(room, input.Text))
		if err != nil {
			return core.InvocationResult{}, err
		}
		delivery = &created
	}
	if err := tx.Commit(); err != nil {
		return core.InvocationResult{}, fmt.Errorf("commit complete invocation: %w", err)
	}
	return core.InvocationResult{Invocation: invocation, Delivery: delivery}, nil
}

func (s *CoreStore) StartCoreInvocation(ctx context.Context, invocationID int64) (core.Invocation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Invocation{}, fmt.Errorf("begin start invocation tx: %w", err)
	}
	defer tx.Rollback()

	invocation, err := updateInvocationRunningTx(ctx, tx, invocationID)
	if err != nil {
		return core.Invocation{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Invocation{}, fmt.Errorf("commit start invocation: %w", err)
	}
	return invocation, nil
}

func (s *CoreStore) ListCoreInvocationContextMessages(ctx context.Context, invocationID int64) ([]core.Message, error) {
	return listInvocationContextMessages(ctx, s.db, invocationID)
}

func (s *CoreStore) ReadCoreInvocationNewMessages(ctx context.Context, invocationID int64) ([]core.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin read new invocation messages tx: %w", err)
	}
	defer tx.Rollback()

	messages, err := readInvocationNewMessagesTx(ctx, tx, invocationID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit read new invocation messages: %w", err)
	}
	return messages, nil
}

func (s *CoreStore) FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (core.InvocationResult, error) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "执行失败，请稍后重试"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.InvocationResult{}, fmt.Errorf("begin fail invocation tx: %w", err)
	}
	defer tx.Rollback()

	invocation, err := updateInvocationFailedTx(ctx, tx, invocationID, detail)
	if err != nil {
		return core.InvocationResult{}, err
	}
	room, err := getCoreRoomByIDTx(ctx, tx, invocation.RoomID)
	if err != nil {
		return core.InvocationResult{}, err
	}
	delivery, err := createCoreDeliveryTx(ctx, tx, invocation.RoomID, invocation.ID, deliveryTextPayload(room, detail))
	if err != nil {
		return core.InvocationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.InvocationResult{}, fmt.Errorf("commit fail invocation: %w", err)
	}
	return core.InvocationResult{Invocation: invocation, Delivery: &delivery}, nil
}

func (s *CoreStore) ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.room_id, d.invocation_id, d.payload, d.status, d.created_at, d.acked_at
		FROM deliveries d
		JOIN rooms r ON r.id = d.room_id
		WHERE r.channel = $1
		  AND d.id > $2
		  AND d.status = $3
		ORDER BY d.id ASC
	`, channel, afterID, core.DeliveryStatusPending)
	if err != nil {
		return nil, fmt.Errorf("list core deliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []core.Delivery
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

func (s *CoreStore) AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error) {
	return ackCoreDelivery(ctx, s.db, id)
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func deliveryTextPayload(room core.Room, text string) json.RawMessage {
	recipientAlias := room.ChannelRoomID
	if room.DisplayName != "" {
		recipientAlias = room.DisplayName
	}
	return mustJSON(map[string]any{
		"type":            "text",
		"text":            text,
		"app":             room.Channel,
		"channel":         room.Channel,
		"channel_room_id": room.ChannelRoomID,
		"recipient_alias": recipientAlias,
	})
}
