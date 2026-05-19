package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	var delivery CoreDelivery
	var ackedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		UPDATE core_deliveries
		SET status = $2,
		    acked_at = NOW()
		WHERE id = $1
		  AND status = $3
		RETURNING id, seq, room_id, invocation_id, payload, status, created_at, acked_at
	`, id, deliveryStatusAcked, deliveryStatusPending).Scan(
		&delivery.ID,
		&delivery.Seq,
		&delivery.RoomID,
		&delivery.InvocationID,
		&delivery.Payload,
		&delivery.Status,
		&delivery.CreatedAt,
		&ackedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CoreDelivery{}, fmt.Errorf("pending delivery %d not found", id)
		}
		return CoreDelivery{}, fmt.Errorf("ack core delivery: %w", err)
	}
	if ackedAt.Valid {
		delivery.AckedAt = ackedAt.Time
	}
	return delivery, nil
}

func validateInboundMessage(input InboundMessageInput) error {
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

func upsertCoreRoomTx(ctx context.Context, tx *sql.Tx, input InboundMessageInput) (CoreRoom, error) {
	var room CoreRoom
	var displayName sql.NullString
	var triggerPolicy []byte
	err := tx.QueryRowContext(ctx, `
		INSERT INTO core_rooms (
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
	`, defaultTenantID, input.Channel, input.ChannelRoomID, input.ChannelRoomType, nullIfEmpty(input.ChannelRoomID)).Scan(
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
		return CoreRoom{}, fmt.Errorf("upsert core room: %w", err)
	}
	if displayName.Valid {
		room.DisplayName = displayName.String
	}
	if len(triggerPolicy) > 0 {
		room.TriggerPolicy = append(json.RawMessage(nil), triggerPolicy...)
	}
	return room, nil
}

func insertCoreMessageTx(ctx context.Context, tx *sql.Tx, roomID int64, input InboundMessageInput) (bool, CoreMessage, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO core_messages (
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
		return false, CoreMessage{}, fmt.Errorf("insert core message: %w", err)
	}
}

func getCoreMessageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (CoreMessage, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
		FROM core_messages
		WHERE id = $1
	`, id)
	return scanCoreMessage(row)
}

func getCoreMessageBySourceTx(ctx context.Context, tx *sql.Tx, roomID int64, sourceMessageID string) (CoreMessage, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
		FROM core_messages
		WHERE room_id = $1
		  AND source_message_id = $2
	`, roomID, sourceMessageID)
	return scanCoreMessage(row)
}

func updateCoreMessageDispatchStateTx(ctx context.Context, tx *sql.Tx, messageID int64, dispatchState int64) (CoreMessage, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE core_messages
		SET dispatch_state = $2
		WHERE id = $1
		RETURNING id, room_id, source_message_id, sender_id, sender_name, payload, message_time, dispatch_state, created_at
	`, messageID, dispatchState)
	return scanCoreMessage(row)
}

func getActiveInvocationForRoomTx(ctx context.Context, tx *sql.Tx, roomID int64) (CoreInvocation, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
		FROM core_invocations
		WHERE room_id = $1
		  AND status IN ($2, $3)
		ORDER BY id DESC
		LIMIT 1
	`, roomID, invocationStatusQueued, invocationStatusRunning)
	invocation, err := scanCoreInvocation(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CoreInvocation{}, false, nil
		}
		return CoreInvocation{}, false, err
	}
	return invocation, true, nil
}

func createInvocationFromWaitingMessagesTx(ctx context.Context, tx *sql.Tx, roomID int64, triggerMessageID int64) (CoreInvocation, error) {
	messageIDs, err := listWaitingMessageIDsTx(ctx, tx, roomID)
	if err != nil {
		return CoreInvocation{}, err
	}
	inputSnapshot := mustJSON(map[string]any{
		"kind":        "initial",
		"message_ids": messageIDs,
	})

	var invocation CoreInvocation
	row := tx.QueryRowContext(ctx, `
		INSERT INTO core_invocations (
			room_id,
			status,
			trigger_message_id,
			input_snapshot
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
	`, roomID, invocationStatusQueued, triggerMessageID, inputSnapshot)
	invocation, err = scanCoreInvocation(row)
	if err != nil {
		return CoreInvocation{}, fmt.Errorf("create core invocation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE core_messages
		SET dispatch_state = $2
		WHERE room_id = $1
		  AND dispatch_state = $3
	`, roomID, invocation.ID, dispatchWaiting); err != nil {
		return CoreInvocation{}, fmt.Errorf("bind waiting messages to invocation: %w", err)
	}
	return invocation, nil
}

func listWaitingMessageIDsTx(ctx context.Context, tx *sql.Tx, roomID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM core_messages
		WHERE room_id = $1
		  AND dispatch_state = $2
		ORDER BY message_time ASC, id ASC
	`, roomID, dispatchWaiting)
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

func updateInvocationTerminalTx(ctx context.Context, tx *sql.Tx, invocationID int64, status string, output json.RawMessage) (CoreInvocation, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE core_invocations
		SET status = $2,
		    output_snapshot = $3,
		    completed_at = NOW()
		WHERE id = $1
		  AND status IN ($4, $5)
		RETURNING id, room_id, status, trigger_message_id, input_snapshot, output_snapshot, created_at, started_at, completed_at
	`, invocationID, status, output, invocationStatusQueued, invocationStatusRunning)
	invocation, err := scanCoreInvocation(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CoreInvocation{}, fmt.Errorf("active invocation %d not found", invocationID)
		}
		return CoreInvocation{}, fmt.Errorf("update invocation terminal: %w", err)
	}
	return invocation, nil
}

func createCoreDeliveryTx(ctx context.Context, tx *sql.Tx, roomID int64, invocationID int64, payload json.RawMessage) (CoreDelivery, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO core_deliveries (
			room_id,
			invocation_id,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id, seq, room_id, invocation_id, payload, status, created_at, acked_at
	`, roomID, invocationID, payload, deliveryStatusPending)
	delivery, err := scanCoreDelivery(row)
	if err != nil {
		return CoreDelivery{}, fmt.Errorf("create core delivery: %w", err)
	}
	return delivery, nil
}

func shouldTriggerCoreMessage(room CoreRoom, input InboundMessageInput) bool {
	if decision, ok := evaluateTriggerPolicy(room.TriggerPolicy, input); ok {
		return decision
	}
	if input.ChannelRoomType == roomChatTypeDirect {
		return true
	}
	var payload struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(input.Payload, &payload)
	text := strings.TrimSpace(payload.Text)
	return strings.Contains(text, "@agent") || strings.Contains(text, "/ask")
}

func evaluateTriggerPolicy(policy json.RawMessage, input InboundMessageInput) (bool, bool) {
	if len(policy) == 0 {
		return false, false
	}
	var parsed struct {
		Mode          string   `json:"mode"`
		Mentions      []string `json:"mentions"`
		Keywords      []string `json:"keywords"`
		DirectDefault *bool    `json:"direct_default"`
	}
	if err := json.Unmarshal(policy, &parsed); err != nil {
		return false, false
	}
	if input.ChannelRoomType == roomChatTypeDirect && parsed.DirectDefault != nil {
		return *parsed.DirectDefault, true
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Mode)) {
	case "always":
		return true, true
	case "never":
		return false, true
	}
	var payload struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(input.Payload, &payload)
	text := payload.Text
	for _, mention := range parsed.Mentions {
		if mention = strings.TrimSpace(mention); mention != "" && strings.Contains(text, mention) {
			return true, true
		}
	}
	for _, keyword := range parsed.Keywords {
		if keyword = strings.TrimSpace(keyword); keyword != "" && strings.Contains(text, keyword) {
			return true, true
		}
	}
	if len(parsed.Mentions) > 0 || len(parsed.Keywords) > 0 || parsed.DirectDefault != nil || parsed.Mode != "" {
		return false, true
	}
	return false, false
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCoreMessage(row scanner) (CoreMessage, error) {
	var message CoreMessage
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
		return CoreMessage{}, err
	}
	if senderName.Valid {
		message.SenderName = senderName.String
	}
	return message, nil
}

func scanCoreInvocation(row scanner) (CoreInvocation, error) {
	var invocation CoreInvocation
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
		return CoreInvocation{}, err
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

func scanCoreDelivery(row scanner) (CoreDelivery, error) {
	var delivery CoreDelivery
	var ackedAt sql.NullTime
	if err := row.Scan(
		&delivery.ID,
		&delivery.Seq,
		&delivery.RoomID,
		&delivery.InvocationID,
		&delivery.Payload,
		&delivery.Status,
		&delivery.CreatedAt,
		&ackedAt,
	); err != nil {
		return CoreDelivery{}, fmt.Errorf("scan core delivery: %w", err)
	}
	if ackedAt.Valid {
		delivery.AckedAt = ackedAt.Time
	}
	return delivery, nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
