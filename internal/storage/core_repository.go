package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tinyclaw/internal/core"
)

func validateInboundMessage(input core.InboundMessageInput) error {
	switch {
	case input.Channel == "":
		return fmt.Errorf("channel is required")
	case input.ChannelRoomID == "":
		return fmt.Errorf("channel_room_id is required")
	case input.ChannelRoomType == "":
		return fmt.Errorf("channel_room_type is required")
	case input.SourceMessageID == "":
		return fmt.Errorf("source_message_id is required")
	case input.SenderID == "":
		return fmt.Errorf("sender_id is required")
	}
	if !json.Valid(input.Payload) {
		return fmt.Errorf("payload must be valid JSON")
	}
	return nil
}

func upsertCoreRoomTx(ctx context.Context, tx *sql.Tx, input core.InboundMessageInput) (core.Room, error) {
	var room core.Room
	var displayName sql.NullString
	var triggerPolicy []byte
	err := tx.QueryRowContext(ctx, `
		INSERT INTO rooms (
			tenant_id,
			channel,
			channel_room_id,
			channel_room_type,
			display_name
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, channel, channel_room_id) DO UPDATE
		SET updated_at = NOW()
		RETURNING id, tenant_id, channel, channel_room_id, channel_room_type, display_name, trigger_policy, created_at, updated_at
	`, core.DefaultTenantID, input.Channel, input.ChannelRoomID, input.ChannelRoomType, nullIfEmpty(input.ChannelRoomID)).Scan(
		&room.ID,
		&room.TenantID,
		&room.Channel,
		&room.ChannelRoomID,
		&room.ChannelRoomType,
		&displayName,
		&triggerPolicy,
		&room.CreatedAt,
		&room.UpdatedAt,
	)
	if err != nil {
		return core.Room{}, fmt.Errorf("upsert core room: %w", err)
	}
	if displayName.Valid {
		room.DisplayName = displayName.String
	}
	if len(triggerPolicy) > 0 {
		room.TriggerPolicy = append(json.RawMessage(nil), triggerPolicy...)
	}
	return room, nil
}

func insertCoreMessageTx(ctx context.Context, tx *sql.Tx, roomID int64, input core.InboundMessageInput) (bool, core.Message, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO messages (
			room_id,
			source_message_id,
			sender_id,
			sender_name,
			payload,
			message_time
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (room_id, source_message_id) DO NOTHING
		RETURNING id
	`, roomID, input.SourceMessageID, input.SenderID, nullIfEmpty(input.SenderName), input.Payload, input.MessageTime).Scan(&id)
	switch {
	case err == nil:
		message, getErr := getCoreMessageByIDTx(ctx, tx, id)
		return true, message, getErr
	case errors.Is(err, sql.ErrNoRows):
		message, getErr := getCoreMessageBySourceTx(ctx, tx, roomID, input.SourceMessageID)
		return false, message, getErr
	default:
		return false, core.Message{}, fmt.Errorf("insert core message: %w", err)
	}
}

func getCoreMessageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
		FROM messages
		WHERE id = $1
	`, id)
	return scanCoreMessage(row)
}

func getCoreMessageBySourceTx(ctx context.Context, tx *sql.Tx, roomID int64, sourceMessageID string) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
		FROM messages
		WHERE room_id = $1
		  AND source_message_id = $2
	`, roomID, sourceMessageID)
	return scanCoreMessage(row)
}

func updateCoreMessageDispatchStateTx(ctx context.Context, tx *sql.Tx, messageID int64, dispatchState int64) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE messages
		SET dispatch_state = $2
		WHERE id = $1
		RETURNING id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
	`, messageID, dispatchState)
	return scanCoreMessage(row)
}

func getActiveInvocationForRoomTx(ctx context.Context, tx *sql.Tx, roomID int64) (core.Invocation, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
		FROM invocations
		WHERE room_id = $1
		  AND status IN ($2, $3)
		ORDER BY id DESC
		LIMIT 1
	`, roomID, core.InvocationStatusQueued, core.InvocationStatusRunning)
	invocation, err := scanCoreInvocation(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Invocation{}, false, nil
		}
		return core.Invocation{}, false, err
	}
	return invocation, true, nil
}

func createInvocationFromWaitingMessagesTx(ctx context.Context, tx *sql.Tx, roomID int64, triggerMessageID int64) (core.Invocation, error) {
	messageIDs, err := listWaitingMessageIDsTx(ctx, tx, roomID)
	if err != nil {
		return core.Invocation{}, err
	}
	inputSnapshot := mustJSON(map[string]any{
		"kind":        "initial",
		"message_ids": messageIDs,
	})

	var invocation core.Invocation
	row := tx.QueryRowContext(ctx, `
		INSERT INTO invocations (
			room_id,
			status,
			trigger_message_id,
			input_snapshot
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
	`, roomID, core.InvocationStatusQueued, triggerMessageID, inputSnapshot)
	invocation, err = scanCoreInvocation(row)
	if err != nil {
		return core.Invocation{}, fmt.Errorf("create core invocation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages
		SET dispatch_state = $2
		WHERE room_id = $1
		  AND dispatch_state = $3
	`, roomID, invocation.ID, core.DispatchWaiting); err != nil {
		return core.Invocation{}, fmt.Errorf("bind waiting messages to invocation: %w", err)
	}
	return invocation, nil
}

func listWaitingMessageIDsTx(ctx context.Context, tx *sql.Tx, roomID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM messages
		WHERE room_id = $1
		  AND dispatch_state = $2
		ORDER BY message_time ASC, id ASC
	`, roomID, core.DispatchWaiting)
	if err != nil {
		return nil, fmt.Errorf("list waiting core messages: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan waiting core message: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate waiting core messages: %w", err)
	}
	return ids, nil
}

func updateInvocationTerminalTx(ctx context.Context, tx *sql.Tx, invocationID int64, status int16, output json.RawMessage) (core.Invocation, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE invocations
		SET status = $2,
		    output_snapshot = $3,
		    completed_at = NOW()
		WHERE id = $1
		  AND status IN ($4, $5)
		RETURNING id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
	`, invocationID, status, output, core.InvocationStatusQueued, core.InvocationStatusRunning)
	invocation, err := scanCoreInvocation(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Invocation{}, fmt.Errorf("active invocation %d not found", invocationID)
		}
		return core.Invocation{}, fmt.Errorf("update invocation terminal: %w", err)
	}
	return invocation, nil
}

func createCoreDeliveryTx(ctx context.Context, tx *sql.Tx, roomID int64, invocationID int64, payload json.RawMessage) (core.Delivery, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO deliveries (
			room_id,
			invocation_id,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id, room_id, invocation_id, payload, status, created_at, acked_at
	`, roomID, invocationID, payload, core.DeliveryStatusPending)
	delivery, err := scanCoreDelivery(row)
	if err != nil {
		return core.Delivery{}, fmt.Errorf("create core delivery: %w", err)
	}
	return delivery, nil
}

func ackCoreDelivery(ctx context.Context, db *sql.DB, id int64) (core.Delivery, error) {
	var delivery core.Delivery
	var ackedAt sql.NullTime
	err := db.QueryRowContext(ctx, `
		UPDATE deliveries
		SET status = $2,
		    acked_at = NOW()
		WHERE id = $1
		  AND status = $3
		RETURNING id, room_id, invocation_id, payload, status, created_at, acked_at
	`, id, core.DeliveryStatusAcked, core.DeliveryStatusPending).Scan(
		&delivery.ID,
		&delivery.RoomID,
		&delivery.InvocationID,
		&delivery.Payload,
		&delivery.Status,
		&delivery.CreatedAt,
		&ackedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Delivery{}, fmt.Errorf("pending delivery %d not found", id)
		}
		return core.Delivery{}, fmt.Errorf("ack core delivery: %w", err)
	}
	if ackedAt.Valid {
		delivery.AckedAt = ackedAt.Time
	}
	return delivery, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCoreMessage(row scanner) (core.Message, error) {
	var message core.Message
	var senderName sql.NullString
	if err := row.Scan(
		&message.ID,
		&message.RoomID,
		&message.SourceMessageID,
		&message.SenderID,
		&senderName,
		&message.Payload,
		&message.MessageTime,
		&message.DispatchState,
		&message.CreatedAt,
	); err != nil {
		return core.Message{}, err
	}
	if senderName.Valid {
		message.SenderName = senderName.String
	}
	return message, nil
}

func scanCoreInvocation(row scanner) (core.Invocation, error) {
	var invocation core.Invocation
	var triggerMessageID sql.NullInt64
	var outputSnapshot []byte
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	if err := row.Scan(
		&invocation.ID,
		&invocation.RoomID,
		&invocation.Status,
		&triggerMessageID,
		&invocation.InputSnapshot,
		&outputSnapshot,
		&invocation.CreatedAt,
		&startedAt,
		&completedAt,
	); err != nil {
		return core.Invocation{}, err
	}
	if triggerMessageID.Valid {
		invocation.TriggerMessageID = triggerMessageID.Int64
	}
	if len(outputSnapshot) > 0 {
		invocation.OutputSnapshot = append(json.RawMessage(nil), outputSnapshot...)
	}
	if startedAt.Valid {
		invocation.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		invocation.CompletedAt = completedAt.Time
	}
	return invocation, nil
}

func scanCoreDelivery(row scanner) (core.Delivery, error) {
	var delivery core.Delivery
	var ackedAt sql.NullTime
	if err := row.Scan(
		&delivery.ID,
		&delivery.RoomID,
		&delivery.InvocationID,
		&delivery.Payload,
		&delivery.Status,
		&delivery.CreatedAt,
		&ackedAt,
	); err != nil {
		return core.Delivery{}, fmt.Errorf("scan core delivery: %w", err)
	}
	if ackedAt.Valid {
		delivery.AckedAt = ackedAt.Time
	}
	return delivery, nil
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
