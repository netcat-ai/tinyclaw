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

func (s *CoreStore) RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error) {
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChannelRoomID = strings.TrimSpace(input.ChannelRoomID)
	input.ChannelRoomType = strings.TrimSpace(input.ChannelRoomType)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.OutboundAlias = strings.TrimSpace(input.OutboundAlias)
	input.AgentKey = strings.TrimSpace(input.AgentKey)
	if input.AgentKey == "" {
		input.AgentKey = core.DefaultAgentKey
	}
	if err := validateRegisterRoom(input); err != nil {
		return core.RegisterRoomResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.RegisterRoomResult{}, fmt.Errorf("begin register room tx: %w", err)
	}
	defer tx.Rollback()

	room, err := upsertRoomTx(ctx, tx, input)
	if err != nil {
		return core.RegisterRoomResult{}, err
	}
	session, err := upsertAgentSessionTx(ctx, tx, room.ID, input)
	if err != nil {
		return core.RegisterRoomResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.RegisterRoomResult{}, fmt.Errorf("commit register room: %w", err)
	}
	return core.RegisterRoomResult{Room: room, AgentSession: session}, nil
}

func (s *CoreStore) CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	input.SourceMessageID = strings.TrimSpace(input.SourceMessageID)
	input.SenderID = strings.TrimSpace(input.SenderID)
	input.SenderName = strings.TrimSpace(input.SenderName)
	if input.MessageTime.IsZero() {
		input.MessageTime = time.Now().UTC()
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	if err := validateCreateMessage(input); err != nil {
		return core.CreateMessageResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("begin create message tx: %w", err)
	}
	defer tx.Rollback()

	room, err := getCoreRoomByIDTx(ctx, tx, input.RoomID)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	inserted, message, err := insertCoreMessageTx(ctx, tx, input)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	result := core.CreateMessageResult{
		Message:   message,
		Duplicate: !inserted,
	}
	if !inserted || input.Skipped {
		if err := tx.Commit(); err != nil {
			return core.CreateMessageResult{}, fmt.Errorf("commit create message: %w", err)
		}
		return result, nil
	}

	sessions, err := listEnabledAgentSessionsForRoomTx(ctx, tx, room.ID)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	for _, session := range sessions {
		if core.ShouldTriggerMessage(room, session, input) {
			if err := markAgentSessionTriggeredTx(ctx, tx, session.ID, message.ID); err != nil {
				return core.CreateMessageResult{}, err
			}
			result.Triggered = true
		}
	}

	if err := tx.Commit(); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("commit create message: %w", err)
	}
	return result, nil
}

func (s *CoreStore) ClaimNextAgentRun(ctx context.Context, owner string, ttl time.Duration) (core.AgentRun, bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return core.AgentRun{}, false, fmt.Errorf("lock owner is required")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AgentRun{}, false, fmt.Errorf("begin claim agent run tx: %w", err)
	}
	defer tx.Rollback()

	run, ok, err := claimNextAgentRunTx(ctx, tx, owner, ttl)
	if err != nil {
		return core.AgentRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return core.AgentRun{}, false, fmt.Errorf("commit claim agent run: %w", err)
	}
	return run, ok, nil
}

func (s *CoreStore) ListAgentRunMessages(ctx context.Context, run core.AgentRun) ([]core.Message, error) {
	return listAgentRunMessages(ctx, s.db, run)
}

func (s *CoreStore) CompleteAgentRun(ctx context.Context, run core.AgentRun, output string) (*core.Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin complete agent run tx: %w", err)
	}
	defer tx.Rollback()

	var payload json.RawMessage
	if strings.TrimSpace(output) != "" {
		room, err := getCoreRoomByIDTx(ctx, tx, run.RoomID)
		if err != nil {
			return nil, err
		}
		payload = deliveryTextPayload(room, "agent_output", output)
	}
	delivery, err := finishAgentRunTx(ctx, tx, run, payload)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit complete agent run: %w", err)
	}
	return delivery, nil
}

func (s *CoreStore) FailAgentRun(ctx context.Context, run core.AgentRun, detail string) (*core.Delivery, error) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "执行失败，请稍后重试"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin fail agent run tx: %w", err)
	}
	defer tx.Rollback()

	room, err := getCoreRoomByIDTx(ctx, tx, run.RoomID)
	if err != nil {
		return nil, err
	}
	delivery, err := finishAgentRunTx(ctx, tx, run, deliveryTextPayload(room, "agent_failure", detail))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit fail agent run: %w", err)
	}
	return delivery, nil
}

func (s *CoreStore) ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.room_id, d.agent_session_id, d.source_message_after_id, d.source_message_until_id, d.payload, d.status, d.created_at, d.acked_at
		FROM deliveries d
		JOIN rooms r ON r.id = d.room_id
		WHERE r.channel = $1
		  AND d.id > $2
		  AND d.status = $3
		ORDER BY d.id ASC
	`, channel, afterID, core.DeliveryStatusPending)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
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
		return nil, fmt.Errorf("iterate deliveries: %w", err)
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

func deliveryTextPayload(room core.Room, kind string, text string) json.RawMessage {
	recipientAlias := room.ChannelRoomID
	if room.DisplayName != "" {
		recipientAlias = room.DisplayName
	}
	if room.OutboundAlias != "" {
		recipientAlias = room.OutboundAlias
	}
	return mustJSON(map[string]any{
		"kind":            kind,
		"type":            "text",
		"text":            text,
		"app":             room.Channel,
		"channel":         room.Channel,
		"channel_room_id": room.ChannelRoomID,
		"recipient_alias": recipientAlias,
	})
}
