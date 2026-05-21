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

func validateRegisterRoom(input core.RegisterRoomInput) error {
	switch {
	case input.Channel == "":
		return fmt.Errorf("channel is required")
	case input.ChannelRoomID == "":
		return fmt.Errorf("channel_room_id is required")
	case input.ChannelRoomType == "":
		return fmt.Errorf("channel_room_type is required")
	case input.OutboundAlias == "":
		return fmt.Errorf("outbound_alias is required")
	}
	if len(input.TriggerPolicy) > 0 && !json.Valid(input.TriggerPolicy) {
		return fmt.Errorf("trigger_policy must be valid JSON")
	}
	return nil
}

func validateCreateMessage(input core.CreateMessageInput) error {
	switch {
	case input.RoomID <= 0:
		return fmt.Errorf("room_id is required")
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

func upsertRoomTx(ctx context.Context, tx *sql.Tx, input core.RegisterRoomInput) (core.Room, error) {
	var room core.Room
	var displayName sql.NullString
	err := tx.QueryRowContext(ctx, `
		INSERT INTO rooms (
			tenant_id,
			channel,
			channel_room_id,
			channel_room_type,
			display_name,
			outbound_alias
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, channel, channel_room_id) DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    outbound_alias = EXCLUDED.outbound_alias,
		    updated_at = NOW()
		RETURNING id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, created_at, updated_at
	`, core.DefaultTenantID, input.Channel, input.ChannelRoomID, input.ChannelRoomType, nullIfEmpty(input.DisplayName), input.OutboundAlias).Scan(
		&room.ID,
		&room.TenantID,
		&room.Channel,
		&room.ChannelRoomID,
		&room.ChannelRoomType,
		&displayName,
		&room.OutboundAlias,
		&room.CreatedAt,
		&room.UpdatedAt,
	)
	if err != nil {
		return core.Room{}, fmt.Errorf("upsert room: %w", err)
	}
	if displayName.Valid {
		room.DisplayName = displayName.String
	}
	return room, nil
}

func upsertAgentSessionTx(ctx context.Context, tx *sql.Tx, roomID int64, input core.RegisterRoomInput) (core.AgentSession, error) {
	agentKey := input.AgentKey
	if agentKey == "" {
		agentKey = core.DefaultAgentKey
	}
	var session core.AgentSession
	var triggerPolicy []byte
	var triggerMessageID sql.NullInt64
	var codexSessionID sql.NullString
	var lockOwner sql.NullString
	var lockExpiresAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_sessions (
			room_id,
			agent_key,
			enabled,
			trigger_policy
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (room_id, agent_key) DO UPDATE
		SET enabled = EXCLUDED.enabled,
		    trigger_policy = EXCLUDED.trigger_policy,
		    updated_at = NOW()
		RETURNING id, room_id, agent_key, enabled, trigger_policy, trigger_message_id, last_processed_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
	`, roomID, agentKey, input.AgentEnabled, nullJSON(input.TriggerPolicy)).Scan(
		&session.ID,
		&session.RoomID,
		&session.AgentKey,
		&session.Enabled,
		&triggerPolicy,
		&triggerMessageID,
		&session.LastProcessedMessageID,
		&codexSessionID,
		&lockOwner,
		&lockExpiresAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	)
	if err != nil {
		return core.AgentSession{}, fmt.Errorf("upsert agent session: %w", err)
	}
	if len(triggerPolicy) > 0 {
		session.TriggerPolicy = append(json.RawMessage(nil), triggerPolicy...)
	}
	if triggerMessageID.Valid {
		session.TriggerMessageID = triggerMessageID.Int64
	}
	if codexSessionID.Valid {
		session.CodexSessionID = codexSessionID.String
	}
	if lockOwner.Valid {
		session.LockOwner = lockOwner.String
	}
	if lockExpiresAt.Valid {
		session.LockExpiresAt = lockExpiresAt.Time
	}
	return session, nil
}

func getCoreRoomByIDTx(ctx context.Context, tx *sql.Tx, id int64) (core.Room, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, created_at, updated_at
		FROM rooms
		WHERE id = $1
	`, id)
	room, err := scanCoreRoom(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Room{}, fmt.Errorf("room %d not found", id)
		}
		return core.Room{}, fmt.Errorf("get room: %w", err)
	}
	return room, nil
}

func insertCoreMessageTx(ctx context.Context, tx *sql.Tx, input core.CreateMessageInput) (bool, core.Message, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO messages (
			room_id,
			source_message_id,
			sender_id,
			sender_name,
			payload,
			message_time,
			skipped
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (room_id, source_message_id) DO NOTHING
		RETURNING id
	`, input.RoomID, input.SourceMessageID, input.SenderID, nullIfEmpty(input.SenderName), input.Payload, input.MessageTime, input.Skipped).Scan(&id)
	switch {
	case err == nil:
		message, getErr := getCoreMessageByIDTx(ctx, tx, id)
		return true, message, getErr
	case errors.Is(err, sql.ErrNoRows):
		message, getErr := getCoreMessageBySourceTx(ctx, tx, input.RoomID, input.SourceMessageID)
		return false, message, getErr
	default:
		return false, core.Message{}, fmt.Errorf("insert message: %w", err)
	}
}

func getCoreMessageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, skipped, created_at
		FROM messages
		WHERE id = $1
	`, id)
	return scanCoreMessage(row)
}

func getCoreMessageBySourceTx(ctx context.Context, tx *sql.Tx, roomID int64, sourceMessageID string) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, skipped, created_at
		FROM messages
		WHERE room_id = $1
		  AND source_message_id = $2
	`, roomID, sourceMessageID)
	return scanCoreMessage(row)
}

func listEnabledAgentSessionsForRoomTx(ctx context.Context, tx *sql.Tx, roomID int64) ([]core.AgentSession, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, room_id, agent_key, enabled, trigger_policy, trigger_message_id, last_processed_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
		FROM agent_sessions
		WHERE room_id = $1
		  AND enabled = TRUE
		ORDER BY id ASC
	`, roomID)
	if err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}
	defer rows.Close()

	var sessions []core.AgentSession
	for rows.Next() {
		session, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent sessions: %w", err)
	}
	return sessions, nil
}

func markAgentSessionTriggeredTx(ctx context.Context, tx *sql.Tx, sessionID int64, messageID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE agent_sessions
		SET trigger_message_id = $2,
		    updated_at = NOW()
		WHERE id = $1
		  AND (trigger_message_id IS NULL OR trigger_message_id < $2)
	`, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("mark agent session triggered: %w", err)
	}
	return nil
}

func claimNextAgentRunTx(ctx context.Context, tx *sql.Tx, owner string, ttl time.Duration) (core.AgentRun, bool, error) {
	var run core.AgentRun
	expiresAt := time.Now().UTC().Add(ttl)
	row := tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT s.id
			FROM agent_sessions s
			WHERE s.enabled = TRUE
			  AND s.trigger_message_id IS NOT NULL
			  AND s.trigger_message_id > s.last_processed_message_id
			  AND (s.lock_expires_at IS NULL OR s.lock_expires_at < NOW())
			ORDER BY s.updated_at ASC, s.id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE agent_sessions s
		SET lock_owner = $1,
		    lock_expires_at = $2,
		    updated_at = NOW()
		FROM candidate
		WHERE s.id = candidate.id
		RETURNING s.id, s.room_id, s.agent_key, s.codex_session_id, s.last_processed_message_id, s.trigger_message_id, s.lock_owner
	`, owner, expiresAt)
	var codexSessionID sql.NullString
	err := row.Scan(
		&run.AgentSessionID,
		&run.RoomID,
		&run.AgentKey,
		&codexSessionID,
		&run.SourceMessageAfterID,
		&run.SourceMessageUntilID,
		&run.LockOwner,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.AgentRun{}, false, nil
		}
		return core.AgentRun{}, false, fmt.Errorf("claim agent run: %w", err)
	}
	if codexSessionID.Valid {
		run.CodexSessionID = codexSessionID.String
	}
	return run, true, nil
}

func listAgentRunMessages(ctx context.Context, db *sql.DB, run core.AgentRun) ([]core.Message, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, skipped, created_at
		FROM messages
		WHERE room_id = $1
		  AND id > $2
		  AND id <= $3
		  AND skipped = FALSE
		ORDER BY id ASC
	`, run.RoomID, run.SourceMessageAfterID, run.SourceMessageUntilID)
	if err != nil {
		return nil, fmt.Errorf("list agent run messages: %w", err)
	}
	defer rows.Close()
	return scanCoreMessages(rows)
}

func finishAgentRunTx(ctx context.Context, tx *sql.Tx, run core.AgentRun, payload json.RawMessage) (*core.Delivery, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_sessions
		SET last_processed_message_id = $2,
		    codex_session_id = COALESCE(NULLIF($4, ''), codex_session_id),
		    lock_owner = NULL,
		    lock_expires_at = NULL,
		    updated_at = NOW()
		WHERE id = $1
		  AND lock_owner = $3
	`, run.AgentSessionID, run.SourceMessageUntilID, run.LockOwner, run.CodexSessionID); err != nil {
		return nil, fmt.Errorf("finish agent run: %w", err)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	delivery, err := createCoreDeliveryTx(ctx, tx, run, payload)
	if err != nil {
		return nil, err
	}
	return &delivery, nil
}

func createCoreDeliveryTx(ctx context.Context, tx *sql.Tx, run core.AgentRun, payload json.RawMessage) (core.Delivery, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO deliveries (
			room_id,
			agent_session_id,
			source_message_after_id,
			source_message_until_id,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, room_id, agent_session_id, source_message_after_id, source_message_until_id, payload, status, created_at, acked_at
	`, run.RoomID, run.AgentSessionID, run.SourceMessageAfterID, run.SourceMessageUntilID, payload, core.DeliveryStatusPending)
	delivery, err := scanCoreDelivery(row)
	if err != nil {
		return core.Delivery{}, fmt.Errorf("create delivery: %w", err)
	}
	return delivery, nil
}

func ackCoreDelivery(ctx context.Context, db *sql.DB, id int64) (core.Delivery, error) {
	row := db.QueryRowContext(ctx, `
		UPDATE deliveries
		SET status = $2,
		    acked_at = NOW()
		WHERE id = $1
		  AND status = $3
		RETURNING id, room_id, agent_session_id, source_message_after_id, source_message_until_id, payload, status, created_at, acked_at
	`, id, core.DeliveryStatusAcked, core.DeliveryStatusPending)
	delivery, err := scanCoreDelivery(row)
	if err != nil {
		return core.Delivery{}, fmt.Errorf("ack delivery: %w", err)
	}
	return delivery, nil
}

func scanCoreMessages(rows *sql.Rows) ([]core.Message, error) {
	var messages []core.Message
	for rows.Next() {
		message, err := scanCoreMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCoreRoom(row scanner) (core.Room, error) {
	var room core.Room
	var displayName sql.NullString
	if err := row.Scan(
		&room.ID,
		&room.TenantID,
		&room.Channel,
		&room.ChannelRoomID,
		&room.ChannelRoomType,
		&displayName,
		&room.OutboundAlias,
		&room.CreatedAt,
		&room.UpdatedAt,
	); err != nil {
		return core.Room{}, err
	}
	if displayName.Valid {
		room.DisplayName = displayName.String
	}
	return room, nil
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
		&message.Skipped,
		&message.CreatedAt,
	); err != nil {
		return core.Message{}, err
	}
	if senderName.Valid {
		message.SenderName = senderName.String
	}
	return message, nil
}

func scanAgentSession(row scanner) (core.AgentSession, error) {
	var session core.AgentSession
	var triggerPolicy []byte
	var triggerMessageID sql.NullInt64
	var codexSessionID sql.NullString
	var lockOwner sql.NullString
	var lockExpiresAt sql.NullTime
	if err := row.Scan(
		&session.ID,
		&session.RoomID,
		&session.AgentKey,
		&session.Enabled,
		&triggerPolicy,
		&triggerMessageID,
		&session.LastProcessedMessageID,
		&codexSessionID,
		&lockOwner,
		&lockExpiresAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		return core.AgentSession{}, err
	}
	if len(triggerPolicy) > 0 {
		session.TriggerPolicy = append(json.RawMessage(nil), triggerPolicy...)
	}
	if triggerMessageID.Valid {
		session.TriggerMessageID = triggerMessageID.Int64
	}
	if codexSessionID.Valid {
		session.CodexSessionID = codexSessionID.String
	}
	if lockOwner.Valid {
		session.LockOwner = lockOwner.String
	}
	if lockExpiresAt.Valid {
		session.LockExpiresAt = lockExpiresAt.Time
	}
	return session, nil
}

func scanCoreDelivery(row scanner) (core.Delivery, error) {
	var delivery core.Delivery
	var ackedAt sql.NullTime
	if err := row.Scan(
		&delivery.ID,
		&delivery.RoomID,
		&delivery.AgentSessionID,
		&delivery.SourceMessageAfterID,
		&delivery.SourceMessageUntilID,
		&delivery.Payload,
		&delivery.Status,
		&delivery.CreatedAt,
		&ackedAt,
	); err != nil {
		return core.Delivery{}, err
	}
	if ackedAt.Valid {
		delivery.AckedAt = ackedAt.Time
	}
	return delivery, nil
}

func nullIfEmpty(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
