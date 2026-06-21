package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"tinyclaw/internal/core"
	"tinyclaw/internal/telemetry"
)

type CoreStore struct {
	db *sql.DB
}

func NewCoreStore(db *sql.DB) *CoreStore {
	return &CoreStore{db: db}
}

func (s *CoreStore) RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error) {
	input = normalizeRegisterRoomInput(input)
	if err := validateRegisterRoom(input); err != nil {
		telemetry.IncRoomRegistration(input.Channel, "error")
		return core.RegisterRoomResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		telemetry.IncRoomRegistration(input.Channel, "error")
		return core.RegisterRoomResult{}, fmt.Errorf("begin register room tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	room, err := upsertRoomTx(ctx, tx, input)
	if err != nil {
		telemetry.IncRoomRegistration(input.Channel, "error")
		return core.RegisterRoomResult{}, err
	}
	session, err := upsertAgentSessionTx(ctx, tx, room.ID, input)
	if err != nil {
		telemetry.IncRoomRegistration(input.Channel, "error")
		return core.RegisterRoomResult{}, err
	}
	if err := tx.Commit(); err != nil {
		telemetry.IncRoomRegistration(input.Channel, "error")
		return core.RegisterRoomResult{}, fmt.Errorf("commit register room: %w", err)
	}
	telemetry.IncRoomRegistration(input.Channel, "success")
	return core.RegisterRoomResult{Room: room, AgentSession: session}, nil
}

func normalizeRegisterRoomInput(input core.RegisterRoomInput) core.RegisterRoomInput {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChannelRoomID = strings.TrimSpace(input.ChannelRoomID)
	input.ChannelRoomType = strings.TrimSpace(input.ChannelRoomType)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.OutboundAlias = strings.TrimSpace(input.OutboundAlias)
	if input.Prompt != nil {
		prompt := strings.TrimSpace(*input.Prompt)
		input.Prompt = &prompt
	}
	return input
}

func (s *CoreStore) GetCoreRoomByID(ctx context.Context, id int64) (core.Room, error) {
	return getCoreRoomByID(ctx, s.db, id)
}

func (s *CoreStore) GetCoreRoomByIdentity(ctx context.Context, tenantID string, channel string, channelRoomID string) (core.Room, bool, error) {
	tenantID = firstNonEmpty(strings.TrimSpace(tenantID), core.DefaultTenantID)
	channel = strings.TrimSpace(channel)
	channelRoomID = strings.TrimSpace(channelRoomID)
	return getCoreRoomByIdentity(ctx, s.db, tenantID, channel, channelRoomID)
}

func (s *CoreStore) CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	input = normalizeCreateMessageInput(input)
	if err := validateCreateMessage(input); err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
		return core.CreateMessageResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
		return core.CreateMessageResult{}, fmt.Errorf("begin create message tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	room, err := getCoreRoomByIDTx(ctx, tx, input.RoomID)
	if err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
		return core.CreateMessageResult{}, err
	}
	inserted, message, err := insertCoreMessageTx(ctx, tx, input)
	if err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
		return core.CreateMessageResult{}, err
	}
	result := core.CreateMessageResult{
		Message:   message,
		Duplicate: !inserted,
	}
	if !inserted || input.SuppressAgentTrigger {
		if err := tx.Commit(); err != nil {
			telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
			return core.CreateMessageResult{}, fmt.Errorf("commit create message: %w", err)
		}
		if !inserted {
			telemetry.IncMessageIngestion(input.Source, input.MsgType, "duplicate", false)
		} else {
			telemetry.IncMessageIngestion(input.Source, input.MsgType, "success", false)
		}
		return result, nil
	}

	sessions, err := listEnabledAgentSessionsForRoomTx(ctx, tx, room.ID)
	if err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
		return core.CreateMessageResult{}, err
	}
	for _, session := range sessions {
		triggered := core.ShouldTriggerMessage(room, session, input)
		if !triggered {
			triggered, err = shouldTriggerBatchMessageTx(ctx, tx, room, session, input, message)
			if err != nil {
				telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
				return core.CreateMessageResult{}, err
			}
		}
		if triggered {
			if err := markAgentSessionTriggeredTx(ctx, tx, session.ID, message.ID); err != nil {
				telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", false)
				return core.CreateMessageResult{}, err
			}
			result.Triggered = true
		}
	}

	if err := tx.Commit(); err != nil {
		telemetry.IncMessageIngestion(input.Source, input.MsgType, "error", result.Triggered)
		return core.CreateMessageResult{}, fmt.Errorf("commit create message: %w", err)
	}
	telemetry.IncMessageIngestion(input.Source, input.MsgType, "success", result.Triggered)
	return result, nil
}

func (s *CoreStore) GetCoreMessageByID(ctx context.Context, id int64) (core.Message, error) {
	if id <= 0 {
		return core.Message{}, fmt.Errorf("message id is required")
	}
	message, err := getCoreMessageByID(ctx, s.db, id)
	if err != nil {
		return core.Message{}, err
	}
	return message, nil
}

func (s *CoreStore) LatestImageMessageBefore(ctx context.Context, roomID int64, beforeMessageID int64) (core.Message, error) {
	if roomID <= 0 {
		return core.Message{}, fmt.Errorf("room id is required")
	}
	if beforeMessageID <= 0 {
		return core.Message{}, fmt.Errorf("before message id is required")
	}
	message, err := latestImageMessageBefore(ctx, s.db, roomID, beforeMessageID)
	if err != nil {
		return core.Message{}, err
	}
	return message, nil
}

func normalizeCreateMessageInput(input core.CreateMessageInput) core.CreateMessageInput {
	input.Source = strings.TrimSpace(input.Source)
	input.MsgID = strings.TrimSpace(input.MsgID)
	input.Action = strings.TrimSpace(input.Action)
	input.FromID = strings.TrimSpace(input.FromID)
	input.RoomIDRaw = strings.TrimSpace(input.RoomIDRaw)
	input.MsgType = strings.TrimSpace(input.MsgType)
	input.SourceMessageID = strings.TrimSpace(input.SourceMessageID)
	input.SenderID = strings.TrimSpace(input.SenderID)
	input.SenderName = strings.TrimSpace(input.SenderName)
	if input.Source == "" {
		input.Source = "external"
	}
	if input.MsgID == "" {
		input.MsgID = input.SourceMessageID
	}
	if input.Action == "" {
		input.Action = "send"
	}
	if input.FromID == "" {
		input.FromID = input.SenderID
	}
	if input.ToList == nil {
		input.ToList = []string{}
	}
	if input.MsgTime <= 0 {
		if input.MessageTime.IsZero() {
			input.MessageTime = time.Now().UTC()
		}
		input.MsgTime = input.MessageTime.UTC().Unix()
	}
	if input.MsgType == "" {
		input.MsgType = "text"
	}
	if len(input.Body) == 0 {
		input.Body = input.Payload
	}
	if len(input.Body) == 0 {
		input.Body = json.RawMessage(`{}`)
	}
	if len(input.Payload) == 0 {
		input.Payload = input.Body
	}
	if input.SourceMessageID == "" {
		input.SourceMessageID = input.MsgID
	}
	if input.SenderID == "" {
		input.SenderID = input.FromID
	}
	if input.MessageTime.IsZero() {
		input.MessageTime = time.Unix(input.MsgTime, 0).UTC()
	}
	return input
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	defer func() { _ = tx.Rollback() }()

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

func (s *CoreStore) CompleteAgentRun(ctx context.Context, run core.AgentRun, result core.AgentRunResult) (*core.Delivery, error) {
	if strings.TrimSpace(result.CodexSessionID) != "" {
		run.CodexSessionID = strings.TrimSpace(result.CodexSessionID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin complete agent run tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var payloads []json.RawMessage
	if strings.TrimSpace(result.FinalOutput) != "" || len(result.GeneratedMediaOutputs) > 0 {
		room, err := getCoreRoomByIDTx(ctx, tx, run.RoomID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(result.FinalOutput) != "" {
			payloads = append(payloads, deliveryTextPayload(room, "agent_output", result.FinalOutput))
		}
		for _, media := range result.GeneratedMediaOutputs {
			payloads = append(payloads, deliveryGeneratedMediaPayload(room, "agent_output", media))
		}
	}
	delivery, err := finishAgentRunTx(ctx, tx, run, nil)
	if err != nil {
		return nil, err
	}
	for _, payload := range payloads {
		created, err := createCoreDeliveryTx(ctx, tx, run, payload)
		if err != nil {
			return nil, err
		}
		if delivery == nil {
			delivery = &created
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit complete agent run: %w", err)
	}
	if err := s.EnqueueMemoryWriteJobs(ctx, run, result.MemoryWriteProposals); err != nil {
		slog.Warn("enqueue memory write jobs failed", "agent_session_id", run.AgentSessionID, "err", err)
	}
	for _, payload := range payloads {
		telemetry.IncDelivery(telemetry.PayloadKind(payload), "created")
	}
	return delivery, nil
}

func (s *CoreStore) CreateGeneratedMediaDelivery(ctx context.Context, run core.AgentRun, media core.GeneratedMediaOutput) (*core.Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generated media delivery tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	room, err := getCoreRoomByIDTx(ctx, tx, run.RoomID)
	if err != nil {
		return nil, err
	}
	payload := deliveryGeneratedMediaPayload(room, "agent_output", media)
	delivery, err := createCoreDeliveryTx(ctx, tx, run, payload)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generated media delivery: %w", err)
	}
	telemetry.IncDelivery(telemetry.PayloadKind(payload), "created")
	return &delivery, nil
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
	defer func() { _ = tx.Rollback() }()

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
	if delivery != nil {
		telemetry.IncDelivery(telemetry.PayloadKind(delivery.Payload), "created")
	}
	return delivery, nil
}

func (s *CoreStore) ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.room_id, d.agent_session_id, d.source_message_from_id, d.source_message_to_id, d.payload, d.status, d.created_at, d.acked_at
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
	defer func() { _ = rows.Close() }()

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
	delivery, err := ackCoreDelivery(ctx, s.db, id)
	if err != nil {
		telemetry.IncDeliveryAck("error")
		return core.Delivery{}, err
	}
	telemetry.IncDeliveryAck("success")
	return delivery, nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func deliveryTextPayload(room core.Room, kind string, text string) json.RawMessage {
	return mustJSON(deliveryRoutePayload(room, map[string]any{
		"kind": kind,
		"type": "text",
		"text": text,
	}))
}

func deliveryGeneratedMediaPayload(room core.Room, kind string, media core.GeneratedMediaOutput) json.RawMessage {
	return mustJSON(deliveryRoutePayload(room, map[string]any{
		"kind":           kind,
		"type":           "image",
		"media_id":       strings.TrimSpace(media.MediaID),
		"media_url":      strings.TrimSpace(media.MediaURL),
		"media_url_kind": strings.TrimSpace(media.MediaURLKind),
		"mime_type":      strings.TrimSpace(media.MIMEType),
		"expires_at":     media.ExpiresAt.UTC().Format(time.RFC3339),
	}))
}

func deliveryRoutePayload(room core.Room, values map[string]any) map[string]any {
	recipientAlias := room.ChannelRoomID
	if room.DisplayName != "" {
		recipientAlias = room.DisplayName
	}
	if room.OutboundAlias != "" {
		recipientAlias = room.OutboundAlias
	}
	values["app"] = room.Channel
	values["channel"] = room.Channel
	values["channel_room_id"] = room.ChannelRoomID
	values["recipient_alias"] = recipientAlias
	return values
}
