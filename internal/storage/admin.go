package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"tinyclaw/internal/core"
)

const (
	defaultAdminListLimit     = 100
	maxAdminListLimit         = 500
	defaultAdminTimelineLimit = 100
	maxAdminTimelineLimit     = 300
)

func (s *CoreStore) ListAdminRooms(ctx context.Context, limit int) ([]core.AdminRoomSummary, error) {
	if limit <= 0 {
		limit = defaultAdminListLimit
	}
	if limit > maxAdminListLimit {
		limit = maxAdminListLimit
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			r.id, r.tenant_id, r.channel, r.channel_room_id, r.channel_room_type, r.display_name, r.outbound_alias, r.created_at, r.updated_at,
			s.id, s.room_id, s.enabled, s.trigger_policy, s.pending_trigger_message_id, s.caught_up_message_id, s.codex_session_id, s.lock_owner, s.lock_expires_at, s.created_at, s.updated_at,
			COALESCE(pending.pending_delivery_count, 0),
			last_message.last_message_time
		FROM rooms r
		LEFT JOIN agent_sessions s ON s.room_id = r.id
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS pending_delivery_count
			FROM deliveries d
			WHERE d.room_id = r.id
			  AND d.status = $1
		) pending ON TRUE
		LEFT JOIN LATERAL (
			SELECT MAX(message_time) AS last_message_time
			FROM messages m
			WHERE m.room_id = r.id
		) last_message ON TRUE
		ORDER BY COALESCE(last_message.last_message_time, r.updated_at) DESC, r.id DESC
		LIMIT $2
	`, core.DeliveryStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("list admin rooms: %w", err)
	}
	defer rows.Close()

	var summaries []core.AdminRoomSummary
	for rows.Next() {
		summary, err := scanAdminRoomSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin rooms: %w", err)
	}
	return summaries, nil
}

func (s *CoreStore) GetAdminRoomTimeline(ctx context.Context, roomID int64, beforeMessageID int64, limit int) (core.AdminRoomTimeline, error) {
	if roomID <= 0 {
		return core.AdminRoomTimeline{}, fmt.Errorf("room_id is required")
	}
	if limit <= 0 {
		limit = defaultAdminTimelineLimit
	}
	if limit > maxAdminTimelineLimit {
		limit = maxAdminTimelineLimit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AdminRoomTimeline{}, fmt.Errorf("begin admin timeline tx: %w", err)
	}
	defer tx.Rollback()

	room, err := getCoreRoomByIDTx(ctx, tx, roomID)
	if err != nil {
		return core.AdminRoomTimeline{}, err
	}
	sessions, err := listAgentSessionsForRoomTx(ctx, tx, roomID)
	if err != nil {
		return core.AdminRoomTimeline{}, err
	}
	messages, hasMore, err := listAdminTimelineMessagesTx(ctx, tx, roomID, beforeMessageID, limit)
	if err != nil {
		return core.AdminRoomTimeline{}, err
	}
	deliveries, err := listAdminTimelineDeliveriesTx(ctx, tx, roomID, messages)
	if err != nil {
		return core.AdminRoomTimeline{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.AdminRoomTimeline{}, fmt.Errorf("commit admin timeline: %w", err)
	}
	return core.AdminRoomTimeline{
		Room:          room,
		AgentSessions: sessions,
		Messages:      messages,
		Deliveries:    deliveries,
		HasMore:       hasMore,
	}, nil
}

func (s *CoreStore) ListAdminRoomMemory(ctx context.Context, input core.AdminMemoryListInput) ([]core.MemoryItem, error) {
	if input.RoomID <= 0 {
		return nil, fmt.Errorf("room_id is required")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultAdminListLimit
	}
	if limit > maxAdminListLimit {
		limit = maxAdminListLimit
	}
	args := []any{input.RoomID}
	conditions := []string{"room_id = $1"}
	status := strings.TrimSpace(input.Status)
	switch status {
	case "", "active":
		args = append(args, core.MemoryStatusActive)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	case "inactive":
		args = append(args, core.MemoryStatusActive)
		conditions = append(conditions, fmt.Sprintf("status <> $%d", len(args)))
	case "all":
	default:
		return nil, fmt.Errorf("invalid status filter")
	}
	if err := validateMemoryTypes(input.Types); err != nil {
		return nil, err
	}
	if len(input.Types) > 0 {
		placeholders := make([]string, 0, len(input.Types))
		for _, typ := range input.Types {
			args = append(args, typ)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		conditions = append(conditions, "type IN ("+strings.Join(placeholders, ", ")+")")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, type, key, content, status, source_message_from_id, source_message_to_id, created_by_agent_session_id, updated_by_agent_session_id, created_at, updated_at
		FROM memory_items
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT $`+fmt.Sprintf("%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list admin room memory: %w", err)
	}
	defer rows.Close()
	return scanMemoryItems(rows)
}

func listAgentSessionsForRoomTx(ctx context.Context, tx *sql.Tx, roomID int64) ([]core.AgentSession, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, room_id, enabled, trigger_policy, pending_trigger_message_id, caught_up_message_id, codex_session_id, lock_owner, lock_expires_at, created_at, updated_at
		FROM agent_sessions
		WHERE room_id = $1
		ORDER BY id ASC
	`, roomID)
	if err != nil {
		return nil, fmt.Errorf("list room agent sessions: %w", err)
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
		return nil, fmt.Errorf("iterate room agent sessions: %w", err)
	}
	return sessions, nil
}

func listAdminTimelineMessagesTx(ctx context.Context, tx *sql.Tx, roomID int64, beforeMessageID int64, limit int) ([]core.Message, bool, error) {
	args := []any{roomID}
	condition := ""
	if beforeMessageID > 0 {
		args = append(args, beforeMessageID)
		condition = fmt.Sprintf("AND id < $%d", len(args))
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, room_id, source_message_id, source, sender_id, sender_name, payload, message_time, created_at
		FROM messages
		WHERE room_id = $1
		`+condition+`
		ORDER BY id DESC
		LIMIT $`+fmt.Sprintf("%d", len(args)), args...)
	if err != nil {
		return nil, false, fmt.Errorf("list admin timeline messages: %w", err)
	}
	defer rows.Close()

	messages, err := scanCoreMessages(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	reverseMessages(messages)
	return messages, hasMore, nil
}

func listAdminTimelineDeliveriesTx(ctx context.Context, tx *sql.Tx, roomID int64, messages []core.Message) ([]core.Delivery, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	fromID := messages[0].ID
	toID := messages[len(messages)-1].ID
	rows, err := tx.QueryContext(ctx, `
		SELECT id, room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status, created_at, acked_at
		FROM deliveries
		WHERE room_id = $1
		  AND (
		    source_message_from_id BETWEEN $2 AND $3
		    OR source_message_to_id BETWEEN $2 AND $3
		    OR (source_message_from_id < $2 AND source_message_to_id > $3)
		  )
		ORDER BY id ASC
	`, roomID, fromID, toID)
	if err != nil {
		return nil, fmt.Errorf("list admin timeline deliveries: %w", err)
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
		return nil, fmt.Errorf("iterate admin timeline deliveries: %w", err)
	}
	return deliveries, nil
}

func scanAdminRoomSummary(row scanner) (core.AdminRoomSummary, error) {
	var summary core.AdminRoomSummary
	var displayName sql.NullString
	var sessionID sql.NullInt64
	var sessionRoomID sql.NullInt64
	var enabled sql.NullBool
	var triggerPolicy []byte
	var pendingTriggerMessageID sql.NullInt64
	var caughtUpMessageID sql.NullInt64
	var codexSessionID sql.NullString
	var lockOwner sql.NullString
	var lockExpiresAt sql.NullTime
	var sessionCreatedAt sql.NullTime
	var sessionUpdatedAt sql.NullTime
	var lastMessageTime sql.NullTime
	if err := row.Scan(
		&summary.Room.ID,
		&summary.Room.TenantID,
		&summary.Room.Channel,
		&summary.Room.ChannelRoomID,
		&summary.Room.ChannelRoomType,
		&displayName,
		&summary.Room.OutboundAlias,
		&summary.Room.CreatedAt,
		&summary.Room.UpdatedAt,
		&sessionID,
		&sessionRoomID,
		&enabled,
		&triggerPolicy,
		&pendingTriggerMessageID,
		&caughtUpMessageID,
		&codexSessionID,
		&lockOwner,
		&lockExpiresAt,
		&sessionCreatedAt,
		&sessionUpdatedAt,
		&summary.PendingDeliveryCount,
		&lastMessageTime,
	); err != nil {
		return core.AdminRoomSummary{}, err
	}
	if displayName.Valid {
		summary.Room.DisplayName = displayName.String
	}
	if sessionID.Valid {
		summary.AgentSession.ID = sessionID.Int64
		summary.AgentSession.RoomID = sessionRoomID.Int64
		summary.AgentSession.Enabled = enabled.Bool
		summary.AgentSession.TriggerPolicy = append(summary.AgentSession.TriggerPolicy, triggerPolicy...)
		summary.AgentSession.CaughtUpMessageID = caughtUpMessageID.Int64
		summary.AgentSession.CreatedAt = sessionCreatedAt.Time
		summary.AgentSession.UpdatedAt = sessionUpdatedAt.Time
		if pendingTriggerMessageID.Valid {
			summary.AgentSession.PendingTriggerMessageID = pendingTriggerMessageID.Int64
		}
		if codexSessionID.Valid {
			summary.AgentSession.CodexSessionID = codexSessionID.String
		}
		if lockOwner.Valid {
			summary.AgentSession.LockOwner = lockOwner.String
		}
		if lockExpiresAt.Valid {
			summary.AgentSession.LockExpiresAt = lockExpiresAt.Time
		}
	}
	if lastMessageTime.Valid {
		summary.LastMessageTime = lastMessageTime.Time
	}
	return summary, nil
}

func reverseMessages(messages []core.Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}
