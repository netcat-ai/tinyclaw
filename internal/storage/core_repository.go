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
	case input.Source == "":
		return fmt.Errorf("source is required")
	case input.MsgID == "":
		return fmt.Errorf("msgid is required")
	case input.FromID == "":
		return fmt.Errorf("from is required")
	case input.MsgType == "":
		return fmt.Errorf("msgtype is required")
	}
	if !json.Valid(input.Body) {
		return fmt.Errorf("body must be valid JSON")
	}
	return nil
}

func upsertRoomTx(ctx context.Context, tx *sql.Tx, input core.RegisterRoomInput) (core.Room, error) {
	var room core.Room
	var displayName sql.NullString
	tenantID := firstNonEmpty(input.TenantID, core.DefaultTenantID)
	prompt := nullStringPtr(input.Prompt)
	if tenantID != core.DefaultTenantID {
		row := tx.QueryRowContext(ctx, `
			UPDATE rooms
			SET tenant_id = $1,
			    channel_room_type = $4,
			    display_name = $5,
			    outbound_alias = $6,
			    prompt = COALESCE($8, prompt),
			    updated_at = NOW()
			WHERE tenant_id = $7
			  AND channel = $2
			  AND channel_room_id = $3
			  AND NOT EXISTS (
			      SELECT 1
			      FROM rooms
			      WHERE tenant_id = $1
			        AND channel = $2
			        AND channel_room_id = $3
			  )
			RETURNING id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
		`, tenantID, input.Channel, input.ChannelRoomID, input.ChannelRoomType, nullIfEmpty(input.DisplayName), input.OutboundAlias, core.DefaultTenantID, prompt)
		room, err := scanCoreRoom(row)
		if err == nil {
			return room, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return core.Room{}, fmt.Errorf("adopt room tenant: %w", err)
		}
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO rooms (
			tenant_id,
			channel,
			channel_room_id,
			channel_room_type,
			display_name,
			outbound_alias,
			prompt
		)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, ''))
		ON CONFLICT (tenant_id, channel, channel_room_id) DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    outbound_alias = EXCLUDED.outbound_alias,
		    prompt = COALESCE($7, rooms.prompt),
		    updated_at = NOW()
		RETURNING id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
	`, tenantID, input.Channel, input.ChannelRoomID, input.ChannelRoomType, nullIfEmpty(input.DisplayName), input.OutboundAlias, prompt).Scan(
		&room.ID,
		&room.TenantID,
		&room.Channel,
		&room.ChannelRoomID,
		&room.ChannelRoomType,
		&displayName,
		&room.OutboundAlias,
		&room.Prompt,
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
	var session core.AgentSession
	var triggerPolicy []byte
	var pendingTriggerMessageID sql.NullInt64
	var codexSessionID sql.NullString
	var lockOwner sql.NullString
	var lockExpiresAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_sessions (
			room_id,
			enabled,
			trigger_policy
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id) DO UPDATE
		SET enabled = EXCLUDED.enabled,
		    trigger_policy = EXCLUDED.trigger_policy,
		    updated_at = NOW()
		RETURNING id, room_id, enabled, trigger_policy, pending_trigger_message_id, caught_up_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
	`, roomID, input.AgentEnabled, nullJSON(input.TriggerPolicy)).Scan(
		&session.ID,
		&session.RoomID,
		&session.Enabled,
		&triggerPolicy,
		&pendingTriggerMessageID,
		&session.CaughtUpMessageID,
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
	if pendingTriggerMessageID.Valid {
		session.PendingTriggerMessageID = pendingTriggerMessageID.Int64
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

func getAgentSessionByRoomIDTx(ctx context.Context, tx *sql.Tx, roomID int64) (core.AgentSession, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, enabled, trigger_policy, pending_trigger_message_id, caught_up_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
		FROM agent_sessions
		WHERE room_id = $1
	`, roomID)
	session, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.AgentSession{}, false, nil
		}
		return core.AgentSession{}, false, fmt.Errorf("get agent session by room: %w", err)
	}
	return session, true, nil
}

func getCoreRoomByIdentityTx(ctx context.Context, tx *sql.Tx, tenantID string, channel string, channelRoomID string) (core.Room, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
		FROM rooms
		WHERE tenant_id = $1
		  AND channel = $2
		  AND channel_room_id = $3
	`, tenantID, channel, channelRoomID)
	room, err := scanCoreRoom(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Room{}, false, nil
		}
		return core.Room{}, false, fmt.Errorf("get room by identity: %w", err)
	}
	return room, true, nil
}

func getCoreRoomByIdentity(ctx context.Context, db *sql.DB, tenantID string, channel string, channelRoomID string) (core.Room, bool, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
		FROM rooms
		WHERE tenant_id = $1
		  AND channel = $2
		  AND channel_room_id = $3
	`, tenantID, channel, channelRoomID)
	room, err := scanCoreRoom(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Room{}, false, nil
		}
		return core.Room{}, false, fmt.Errorf("get room by identity: %w", err)
	}
	return room, true, nil
}

func getCoreRoomByIDTx(ctx context.Context, tx *sql.Tx, id int64) (core.Room, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
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

func getCoreRoomByID(ctx context.Context, db *sql.DB, id int64) (core.Room, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias, prompt, created_at, updated_at
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
			source,
			msgid,
			action,
			from_id,
			tolist,
			roomid,
			msgtime,
			msgtype,
			body
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (room_id, source, msgid) DO NOTHING
		RETURNING id
	`, input.RoomID, input.Source, input.MsgID, input.Action, input.FromID, mustJSON(input.ToList), input.RoomIDRaw, input.MsgTime, input.MsgType, input.Body).Scan(&id)
	switch {
	case err == nil:
		message, getErr := getCoreMessageByIDTx(ctx, tx, id)
		return true, message, getErr
	case errors.Is(err, sql.ErrNoRows):
		message, getErr := getCoreMessageBySourceTx(ctx, tx, input.RoomID, input.Source, input.MsgID)
		return false, message, getErr
	default:
		return false, core.Message{}, fmt.Errorf("insert message: %w", err)
	}
}

func getCoreMessageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source, msgid, action, from_id, tolist, roomid, msgtime, msgtype, body, created_at
		FROM messages
		WHERE id = $1
	`, id)
	return scanCoreMessage(row)
}

func getCoreMessageByID(ctx context.Context, db *sql.DB, id int64) (core.Message, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, room_id, source, msgid, action, from_id, tolist, roomid, msgtime, msgtype, body, created_at
		FROM messages
		WHERE id = $1
	`, id)
	message, err := scanCoreMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Message{}, fmt.Errorf("message %d not found", id)
		}
		return core.Message{}, fmt.Errorf("get message: %w", err)
	}
	return message, nil
}

func getCoreMessageBySourceTx(ctx context.Context, tx *sql.Tx, roomID int64, source string, msgID string) (core.Message, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source, msgid, action, from_id, tolist, roomid, msgtime, msgtype, body, created_at
		FROM messages
		WHERE room_id = $1
		  AND source = $2
		  AND msgid = $3
	`, roomID, source, msgID)
	return scanCoreMessage(row)
}

func latestImageMessageBefore(ctx context.Context, db *sql.DB, roomID int64, beforeMessageID int64) (core.Message, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, room_id, source, msgid, action, from_id, tolist, roomid, msgtime, msgtype, body, created_at
		FROM messages
		WHERE room_id = $1
		  AND id < $2
		  AND msgtype = 'image'
		ORDER BY id DESC
		LIMIT 1
	`, roomID, beforeMessageID)
	message, err := scanCoreMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Message{}, fmt.Errorf("previous image message not found")
		}
		return core.Message{}, fmt.Errorf("get previous image message: %w", err)
	}
	return message, nil
}

func listEnabledAgentSessionsForRoomTx(ctx context.Context, tx *sql.Tx, roomID int64) ([]core.AgentSession, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, room_id, enabled, trigger_policy, pending_trigger_message_id, caught_up_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
		FROM agent_sessions
		WHERE room_id = $1
		  AND enabled = TRUE
		ORDER BY id ASC
	`, roomID)
	if err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
		SET pending_trigger_message_id = $2,
		    updated_at = NOW()
		WHERE id = $1
		  AND (pending_trigger_message_id IS NULL OR pending_trigger_message_id < $2)
	`, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("mark agent session triggered: %w", err)
	}
	return nil
}

func shouldTriggerBatchMessageTx(ctx context.Context, tx *sql.Tx, room core.Room, session core.AgentSession, input core.CreateMessageInput, message core.Message) (bool, error) {
	policy, ok := core.EvaluateBatchTriggerPolicy(session.TriggerPolicy, room.ChannelRoomType, input)
	if !ok {
		return false, nil
	}
	if policy.MinMessages <= 0 && policy.MaxIntervalSeconds <= 0 {
		return false, nil
	}
	boundaryID := session.CaughtUpMessageID
	if session.PendingTriggerMessageID > boundaryID {
		boundaryID = session.PendingTriggerMessageID
	}
	state, err := getBatchTriggerStateTx(ctx, tx, room.ID, boundaryID, message.ID)
	if err != nil {
		return false, err
	}
	if state.MessageCount <= 0 {
		return false, nil
	}
	if policy.MinMessages > 0 && state.MessageCount >= policy.MinMessages {
		if policy.MinIntervalSeconds <= 0 || boundaryID == 0 || state.CurrentCreatedAt.Sub(state.BoundaryCreatedAt) >= time.Duration(policy.MinIntervalSeconds)*time.Second {
			return true, nil
		}
	}
	if policy.MaxIntervalSeconds > 0 && state.CurrentCreatedAt.Sub(state.FirstMessageCreatedAt) >= time.Duration(policy.MaxIntervalSeconds)*time.Second {
		return true, nil
	}
	return false, nil
}

type batchTriggerState struct {
	MessageCount          int
	FirstMessageCreatedAt time.Time
	BoundaryCreatedAt     time.Time
	CurrentCreatedAt      time.Time
}

func getBatchTriggerStateTx(ctx context.Context, tx *sql.Tx, roomID int64, boundaryID int64, currentMessageID int64) (batchTriggerState, error) {
	row := tx.QueryRowContext(ctx, `
		WITH pending AS (
			SELECT COUNT(*)::int AS message_count,
			       MIN(created_at) AS first_message_created_at
			FROM messages
			WHERE room_id = $1
			  AND id > $2
			  AND id <= $3
		),
		boundary AS (
			SELECT created_at AS boundary_created_at
			FROM messages
			WHERE room_id = $1
			  AND id = $2
		),
		current_message AS (
			SELECT created_at AS current_created_at
			FROM messages
			WHERE room_id = $1
			  AND id = $3
		)
		SELECT pending.message_count,
		       pending.first_message_created_at,
		       COALESCE(boundary.boundary_created_at, pending.first_message_created_at),
		       current_message.current_created_at
		FROM pending
		CROSS JOIN current_message
		LEFT JOIN boundary ON TRUE
	`, roomID, boundaryID, currentMessageID)
	var state batchTriggerState
	var firstMessageCreatedAt sql.NullTime
	var boundaryCreatedAt sql.NullTime
	if err := row.Scan(
		&state.MessageCount,
		&firstMessageCreatedAt,
		&boundaryCreatedAt,
		&state.CurrentCreatedAt,
	); err != nil {
		return batchTriggerState{}, fmt.Errorf("get batch trigger state: %w", err)
	}
	if firstMessageCreatedAt.Valid {
		state.FirstMessageCreatedAt = firstMessageCreatedAt.Time
	}
	if boundaryCreatedAt.Valid {
		state.BoundaryCreatedAt = boundaryCreatedAt.Time
	}
	return state, nil
}

func claimNextAgentRunTx(ctx context.Context, tx *sql.Tx, owner string, ttl time.Duration) (core.AgentRun, bool, error) {
	var run core.AgentRun
	expiresAt := time.Now().UTC().Add(ttl)
	row := tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT s.id,
			       r.prompt AS room_prompt
			FROM agent_sessions s
			JOIN rooms r ON r.id = s.room_id
			WHERE s.enabled = TRUE
			  AND s.pending_trigger_message_id IS NOT NULL
			  AND s.pending_trigger_message_id > s.caught_up_message_id
			  AND (s.lock_expires_at IS NULL OR s.lock_expires_at < NOW())
			ORDER BY s.updated_at ASC, s.id ASC
			LIMIT 1
			FOR UPDATE OF s SKIP LOCKED
		)
		UPDATE agent_sessions s
		SET lock_owner = $1,
		    lock_expires_at = $2,
		    updated_at = NOW()
		FROM candidate
		WHERE s.id = candidate.id
		RETURNING s.id,
		          s.room_id,
		          s.codex_session_id,
		          COALESCE((
		              SELECT MIN(m.id)
		              FROM messages m
		              WHERE m.room_id = s.room_id
		                AND m.id > s.caught_up_message_id
		                AND m.id <= s.pending_trigger_message_id
		          ), s.pending_trigger_message_id),
		          s.pending_trigger_message_id,
		          s.lock_owner,
		          candidate.room_prompt
	`, owner, expiresAt)
	var codexSessionID sql.NullString
	var roomPrompt string
	err := row.Scan(
		&run.AgentSessionID,
		&run.RoomID,
		&codexSessionID,
		&run.SourceMessageFromID,
		&run.SourceMessageToID,
		&run.LockOwner,
		&roomPrompt,
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
	run.RoomPrompt = roomPrompt
	return run, true, nil
}

func listAgentRunMessages(ctx context.Context, db *sql.DB, run core.AgentRun) ([]core.Message, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, room_id, source, msgid, action, from_id, tolist, roomid, msgtime, msgtype, body, created_at
		FROM messages
		WHERE room_id = $1
		  AND id >= $2
		  AND id <= $3
		ORDER BY id ASC
	`, run.RoomID, run.SourceMessageFromID, run.SourceMessageToID)
	if err != nil {
		return nil, fmt.Errorf("list agent run messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanCoreMessages(rows)
}

func finishAgentRunTx(ctx context.Context, tx *sql.Tx, run core.AgentRun, payload json.RawMessage) (*core.Delivery, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_sessions
		SET caught_up_message_id = $2,
		    codex_session_id = COALESCE(NULLIF($4, ''), codex_session_id),
		    lock_owner = NULL,
		    lock_expires_at = NULL,
		    updated_at = NOW()
		WHERE id = $1
		  AND lock_owner = $3
	`, run.AgentSessionID, run.SourceMessageToID, run.LockOwner, run.CodexSessionID)
	if err != nil {
		return nil, fmt.Errorf("finish agent run: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("finish agent run rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return nil, fmt.Errorf("agent session lock is no longer held")
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
			source_message_from_id,
			source_message_to_id,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status, created_at, acked_at
	`, run.RoomID, nullInt64IfZero(run.AgentSessionID), run.SourceMessageFromID, run.SourceMessageToID, payload, core.DeliveryStatusPending)
	delivery, err := scanCoreDelivery(row)
	if err != nil {
		return core.Delivery{}, fmt.Errorf("create delivery: %w", err)
	}
	return delivery, nil
}

func ackCoreDelivery(ctx context.Context, db *sql.DB, id int64) (core.Delivery, error) {
	row := db.QueryRowContext(ctx, `
		WITH updated AS (
			UPDATE deliveries
			SET status = $2,
			    acked_at = COALESCE(acked_at, NOW())
			WHERE id = $1
			  AND status = $3
			RETURNING id, room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status, created_at, acked_at
		)
		SELECT id, room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status, created_at, acked_at
		FROM updated
		UNION ALL
		SELECT id, room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status, created_at, acked_at
		FROM deliveries
		WHERE id = $1
		  AND status = $2
		  AND NOT EXISTS (SELECT 1 FROM updated)
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
		&room.Prompt,
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
	if err := row.Scan(
		&message.ID,
		&message.RoomID,
		&message.Source,
		&message.MsgID,
		&message.Action,
		&message.FromID,
		&message.ToList,
		&message.RoomIDRaw,
		&message.MsgTime,
		&message.MsgType,
		&message.Body,
		&message.CreatedAt,
	); err != nil {
		return core.Message{}, err
	}
	message.SourceMessageID = message.MsgID
	message.SenderID = message.FromID
	message.SenderName = message.FromName
	message.Payload = message.Body
	message.MessageTime = time.Unix(message.MsgTime, 0).UTC()
	return message, nil
}

func scanAgentSession(row scanner) (core.AgentSession, error) {
	var session core.AgentSession
	var triggerPolicy []byte
	var pendingTriggerMessageID sql.NullInt64
	var codexSessionID sql.NullString
	var lockOwner sql.NullString
	var lockExpiresAt sql.NullTime
	if err := row.Scan(
		&session.ID,
		&session.RoomID,
		&session.Enabled,
		&triggerPolicy,
		&pendingTriggerMessageID,
		&session.CaughtUpMessageID,
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
	if pendingTriggerMessageID.Valid {
		session.PendingTriggerMessageID = pendingTriggerMessageID.Int64
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
	var agentSessionID sql.NullInt64
	var ackedAt sql.NullTime
	if err := row.Scan(
		&delivery.ID,
		&delivery.RoomID,
		&agentSessionID,
		&delivery.SourceMessageFromID,
		&delivery.SourceMessageToID,
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
	if agentSessionID.Valid {
		delivery.AgentSessionID = agentSessionID.Int64
	}
	return delivery, nil
}

func nullIfEmpty(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullStringPtr(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func nullJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullInt64IfZero(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: value != 0}
}
