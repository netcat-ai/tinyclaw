package msghub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "embed"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var postgresSchema string

type PostgresStore struct {
	db *sql.DB
}

func OpenPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", strings.TrimSpace(dsn))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return NewPostgresStore(db), nil
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) DB() *sql.DB {
	return s.db
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) InitSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, postgresSchema)
	return err
}

func (s *PostgresStore) SeedActor(ctx context.Context, input Actor) (Actor, error) {
	actor := normalizeActor(input)
	if actor.Token == "" {
		return Actor{}, fmt.Errorf("actor token is required")
	}
	permissions, err := json.Marshal(actor.Permissions)
	if err != nil {
		return Actor{}, err
	}
	return scanActor(s.db.QueryRowContext(ctx, `
		INSERT INTO actors (
			actor_key, token_hash, kind, name, enabled,
			channel, provider_account_id, permissions
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (actor_key) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			kind = EXCLUDED.kind,
			name = EXCLUDED.name,
			enabled = EXCLUDED.enabled,
			channel = EXCLUDED.channel,
			provider_account_id = EXCLUDED.provider_account_id,
			permissions = EXCLUDED.permissions,
			updated_at = NOW()
		RETURNING id, actor_key, kind, name, enabled, channel, provider_account_id, permissions, created_at, updated_at
	`, actor.ActorKey, tokenHash(actor.Token), actor.Kind, actor.Name, actor.Enabled,
		actor.Channel, actor.ProviderAccountID, permissions))
}

func (s *PostgresStore) SubscribeActorToRoom(ctx context.Context, actorID int64, roomID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO actor_subscriptions (actor_id, room_id)
		VALUES ($1, $2)
		ON CONFLICT (actor_id, room_id) DO NOTHING
	`, actorID, roomID)
	return err
}

func (s *PostgresStore) Authenticate(ctx context.Context, token string) (Actor, bool, error) {
	actor, err := scanActor(s.db.QueryRowContext(ctx, `
		SELECT id, actor_key, kind, name, enabled, channel, provider_account_id, permissions, created_at, updated_at
		FROM actors
		WHERE token_hash = $1
	`, tokenHash(token)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, false, nil
		}
		return Actor{}, false, err
	}
	if !actor.Enabled {
		return Actor{}, false, nil
	}
	return actor, true, nil
}

func (s *PostgresStore) AppendTerminalMessage(ctx context.Context, actor Actor, input SendMessageRequest) (Message, bool, error) {
	if actor.Kind != ActorKindTerminal {
		return Message{}, false, fmt.Errorf("terminal actor is required")
	}
	if strings.TrimSpace(input.ExternalMessageID) == "" {
		return Message{}, false, fmt.Errorf("external_message_id is required")
	}
	if strings.TrimSpace(input.SenderID) == "" {
		return Message{}, false, fmt.Errorf("sender_id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	room, err := upsertPostgresRoom(ctx, tx, actor, input.Room)
	if err != nil {
		return Message{}, false, err
	}
	message, inserted, err := insertPostgresMessage(ctx, tx, actor, room, input)
	if err != nil {
		return Message{}, false, err
	}
	if inserted {
		if err := enqueuePostgresMessageReceived(ctx, tx, room, message); err != nil {
			return Message{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Message{}, false, err
	}
	return message, !inserted, nil
}

func (s *PostgresStore) CreateReplyTask(ctx context.Context, actor Actor, input SendMessageRequest) (ActorTask, error) {
	if actor.Kind != ActorKindAIAgent {
		return ActorTask{}, fmt.Errorf("aiagent actor is required")
	}
	tokenValue := strings.TrimSpace(input.ContextToken)
	if tokenValue == "" {
		return ActorTask{}, fmt.Errorf("context_token is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ActorTask{}, err
	}
	defer func() { _ = tx.Rollback() }()

	token, err := getCapabilityTokenForUpdate(ctx, tx, tokenValue)
	if err != nil {
		return ActorTask{}, err
	}
	if token.ActorID != actor.ID || token.Action != CapabilityReply {
		return ActorTask{}, fmt.Errorf("context_token is not valid for this actor")
	}
	if token.UsesRemaining <= 0 || time.Now().UTC().After(token.ExpiresAt) {
		return ActorTask{}, fmt.Errorf("context_token expired")
	}
	room, err := getPostgresRoomByID(ctx, tx, token.RoomID)
	if err != nil {
		return ActorTask{}, err
	}
	assignee, err := findPostgresTerminalForRoom(ctx, tx, room)
	if err != nil {
		return ActorTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capability_tokens
		SET uses_remaining = uses_remaining - 1
		WHERE token = $1
	`, token.Token); err != nil {
		return ActorTask{}, err
	}

	messageType := strings.TrimSpace(input.MessageType)
	if messageType == "" {
		messageType = "text"
	}
	payload, err := marshalJSON(map[string]any{
		"room":                roomToView(room),
		"message_type":        messageType,
		"body":                json.RawMessage(cloneJSON(input.Body)),
		"reply_to_message_id": token.MessageID,
	})
	if err != nil {
		return ActorTask{}, err
	}
	task, err := scanActorTask(tx.QueryRowContext(ctx, `
		INSERT INTO actor_tasks (
			room_id, requester_actor_id, assignee_actor_id, task_type, payload, status
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, room_id, requester_actor_id, assignee_actor_id, task_type, payload, status, created_at, acked_at
	`, room.ID, actor.ID, assignee.ID, TaskTypeSendMessage, payload, TaskStatusPending))
	if err != nil {
		return ActorTask{}, err
	}
	if err := enqueuePostgresInbox(ctx, tx, assignee.ID, EventMessageSendRequested, ResourceActorTask, task.ID); err != nil {
		return ActorTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActorTask{}, err
	}
	return task, nil
}

func (s *PostgresStore) ListUpdates(ctx context.Context, actor Actor, afterID int64, limit int) ([]UpdateView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, actor_id, event_type, resource_type, resource_id, created_at, acked_at
		FROM actor_inbox
		WHERE actor_id = $1 AND id > $2
		ORDER BY id ASC
		LIMIT $3
	`, actor.ID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var updates []UpdateView
	for rows.Next() {
		item, err := scanInboxItem(rows)
		if err != nil {
			return nil, err
		}
		view, err := s.updateView(ctx, item)
		if err != nil {
			return nil, err
		}
		updates = append(updates, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func (s *PostgresStore) Ack(ctx context.Context, actor Actor, input AckRequest) (AckResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AckResponse{}, err
	}
	defer func() { _ = tx.Rollback() }()

	response := AckResponse{
		AckedUpdateIDs: []int64{},
		Tasks:          []ActorTaskView{},
	}
	for _, id := range input.UpdateIDs {
		var ackedID int64
		err := tx.QueryRowContext(ctx, `
			UPDATE actor_inbox
			SET acked_at = COALESCE(acked_at, NOW())
			WHERE id = $1 AND actor_id = $2
			RETURNING id
		`, id, actor.ID).Scan(&ackedID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return response, err
		}
		response.AckedUpdateIDs = append(response.AckedUpdateIDs, ackedID)
	}
	for _, result := range input.TaskResults {
		task, err := getPostgresTaskForUpdate(ctx, tx, result.TaskID)
		if err != nil {
			return response, err
		}
		if task.AssigneeActorID != actor.ID {
			return response, fmt.Errorf("task %d is not assigned to actor", result.TaskID)
		}
		previousStatus := task.Status
		var eventType string
		switch strings.TrimSpace(result.Status) {
		case TaskStatusAcked:
			task.Status = TaskStatusAcked
			eventType = EventMessageSent
		case TaskStatusFailed:
			task.Status = TaskStatusFailed
			task.Payload = payloadWithError(task.Payload, result.Error)
			eventType = EventMessageFailed
		default:
			return response, fmt.Errorf("unsupported task status %q", result.Status)
		}
		if previousStatus != TaskStatusPending {
			if previousStatus == task.Status {
				response.Tasks = append(response.Tasks, taskToView(task))
				continue
			}
			return response, fmt.Errorf("task %d is already %s", result.TaskID, previousStatus)
		}
		task, err = scanActorTask(tx.QueryRowContext(ctx, `
				UPDATE actor_tasks
				SET status = $2, payload = $3, acked_at = NOW()
				WHERE id = $1
				RETURNING id, room_id, requester_actor_id, assignee_actor_id, task_type, payload, status, created_at, acked_at
		`, task.ID, task.Status, task.Payload))
		if err != nil {
			return response, err
		}
		if err := enqueuePostgresInbox(ctx, tx, task.RequesterActorID, eventType, ResourceActorTask, task.ID); err != nil {
			return response, err
		}
		response.Tasks = append(response.Tasks, taskToView(task))
	}
	if err := tx.Commit(); err != nil {
		return response, err
	}
	return response, nil
}

func (s *PostgresStore) updateView(ctx context.Context, item InboxItem) (UpdateView, error) {
	payload, err := s.payloadForInboxItem(ctx, item)
	if err != nil {
		return UpdateView{}, err
	}
	view := UpdateView{
		ID:           item.ID,
		EventType:    item.EventType,
		ResourceType: item.ResourceType,
		ResourceID:   item.ResourceID,
		Payload:      payload,
		CreatedAt:    item.CreatedAt,
	}
	if !item.AckedAt.IsZero() {
		ackedAt := item.AckedAt
		view.AckedAt = &ackedAt
	}
	return view, nil
}

func (s *PostgresStore) payloadForInboxItem(ctx context.Context, item InboxItem) (json.RawMessage, error) {
	switch item.ResourceType {
	case ResourceMessage:
		message, err := getPostgresMessageByID(ctx, s.db, item.ResourceID)
		if err != nil {
			return nil, err
		}
		room, err := getPostgresRoomByID(ctx, s.db, message.RoomID)
		if err != nil {
			return nil, err
		}
		contextToken, err := getReplyToken(ctx, s.db, item.ActorID, message.ID)
		if err != nil {
			return nil, err
		}
		return marshalJSON(map[string]any{
			"room":          roomToView(room),
			"message":       messageToView(message),
			"context_token": contextToken,
		})
	case ResourceActorTask:
		task, err := getPostgresTaskByID(ctx, s.db, item.ResourceID)
		if err != nil {
			return nil, err
		}
		room, err := getPostgresRoomByID(ctx, s.db, task.RoomID)
		if err != nil {
			return nil, err
		}
		return marshalJSON(map[string]any{
			"room": roomToView(room),
			"task": taskToView(task),
		})
	default:
		return nil, fmt.Errorf("unsupported resource type %q", item.ResourceType)
	}
}

func normalizeActor(input Actor) Actor {
	actor := input
	actor.ActorKey = strings.TrimSpace(actor.ActorKey)
	actor.Kind = strings.TrimSpace(actor.Kind)
	actor.Name = strings.TrimSpace(actor.Name)
	actor.Channel = strings.TrimSpace(actor.Channel)
	actor.ProviderAccountID = strings.TrimSpace(actor.ProviderAccountID)
	actor.Token = strings.TrimSpace(actor.Token)
	if actor.ActorKey == "" {
		actor.ActorKey = "actor"
	}
	if actor.Name == "" {
		actor.Name = actor.ActorKey
	}
	if !actor.Enabled {
		actor.Enabled = true
	}
	return actor
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanActor(row rowScanner) (Actor, error) {
	var actor Actor
	var permissions []byte
	err := row.Scan(
		&actor.ID,
		&actor.ActorKey,
		&actor.Kind,
		&actor.Name,
		&actor.Enabled,
		&actor.Channel,
		&actor.ProviderAccountID,
		&permissions,
		&actor.CreatedAt,
		&actor.UpdatedAt,
	)
	if err != nil {
		return Actor{}, err
	}
	if len(permissions) > 0 {
		if err := json.Unmarshal(permissions, &actor.Permissions); err != nil {
			return Actor{}, err
		}
	}
	return actor, nil
}

func scanRoom(row rowScanner) (Room, error) {
	var room Room
	err := row.Scan(
		&room.ID,
		&room.TenantID,
		&room.Channel,
		&room.ProviderAccountID,
		&room.ExternalRoomID,
		&room.RoomType,
		&room.DisplayName,
		&room.OutboundTarget,
		&room.CreatedAt,
		&room.UpdatedAt,
	)
	if err != nil {
		return Room{}, err
	}
	return room, nil
}

func scanMessage(row rowScanner) (Message, error) {
	var message Message
	var body []byte
	err := row.Scan(
		&message.ID,
		&message.RoomID,
		&message.SourceActorID,
		&message.ExternalMessageID,
		&message.SenderID,
		&message.SenderName,
		&message.MessageTime,
		&message.MessageType,
		&body,
		&message.CreatedAt,
	)
	if err != nil {
		return Message{}, err
	}
	message.Body = cloneJSON(body)
	return message, nil
}

func scanActorTask(row rowScanner) (ActorTask, error) {
	var task ActorTask
	var payload []byte
	var ackedAt sql.NullTime
	err := row.Scan(
		&task.ID,
		&task.RoomID,
		&task.RequesterActorID,
		&task.AssigneeActorID,
		&task.TaskType,
		&payload,
		&task.Status,
		&task.CreatedAt,
		&ackedAt,
	)
	if err != nil {
		return ActorTask{}, err
	}
	task.Payload = cloneJSON(payload)
	if ackedAt.Valid {
		task.AckedAt = ackedAt.Time
	}
	return task, nil
}

func scanInboxItem(row rowScanner) (InboxItem, error) {
	var item InboxItem
	var ackedAt sql.NullTime
	err := row.Scan(
		&item.ID,
		&item.ActorID,
		&item.EventType,
		&item.ResourceType,
		&item.ResourceID,
		&item.CreatedAt,
		&ackedAt,
	)
	if err != nil {
		return InboxItem{}, err
	}
	if ackedAt.Valid {
		item.AckedAt = ackedAt.Time
	}
	return item, nil
}

func scanCapabilityToken(row rowScanner) (CapabilityToken, error) {
	var token CapabilityToken
	var messageID sql.NullInt64
	var actorTaskID sql.NullInt64
	err := row.Scan(
		&token.Token,
		&token.ActorID,
		&token.RoomID,
		&token.Action,
		&messageID,
		&actorTaskID,
		&token.UsesRemaining,
		&token.ExpiresAt,
		&token.CreatedAt,
	)
	if err != nil {
		return CapabilityToken{}, err
	}
	if messageID.Valid {
		token.MessageID = messageID.Int64
	}
	if actorTaskID.Valid {
		token.ActorTaskID = actorTaskID.Int64
	}
	return token, nil
}

func upsertPostgresRoom(ctx context.Context, tx *sql.Tx, actor Actor, input RoomInput) (Room, error) {
	tenantID := firstNonEmpty(input.TenantID, DefaultTenantID)
	channel := firstNonEmpty(input.Channel, actor.Channel)
	providerAccountID := firstNonEmpty(input.ProviderAccountID, actor.ProviderAccountID)
	externalRoomID := strings.TrimSpace(input.ExternalRoomID)
	roomType := strings.TrimSpace(input.RoomType)
	if channel == "" {
		return Room{}, fmt.Errorf("room.channel is required")
	}
	if providerAccountID == "" {
		return Room{}, fmt.Errorf("room.provider_account_id is required")
	}
	if externalRoomID == "" {
		return Room{}, fmt.Errorf("room.external_room_id is required")
	}
	if roomType == "" {
		roomType = "direct"
	}
	if roomType != "direct" && roomType != "group" {
		return Room{}, fmt.Errorf("room.room_type must be direct or group")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	outboundTarget := strings.TrimSpace(input.OutboundTarget)
	if outboundTarget == "" {
		outboundTarget = externalRoomID
	}
	return scanRoom(tx.QueryRowContext(ctx, `
		INSERT INTO rooms (
			tenant_id, channel, provider_account_id, external_room_id,
			room_type, display_name, outbound_target
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, channel, provider_account_id, external_room_id) DO UPDATE SET
			room_type = EXCLUDED.room_type,
			display_name = CASE
				WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name
				ELSE rooms.display_name
			END,
			outbound_target = CASE
				WHEN EXCLUDED.outbound_target <> '' THEN EXCLUDED.outbound_target
				ELSE rooms.outbound_target
			END,
			updated_at = NOW()
		RETURNING id, tenant_id, channel, provider_account_id, external_room_id,
			room_type, display_name, outbound_target, created_at, updated_at
	`, tenantID, channel, providerAccountID, externalRoomID, roomType, displayName, outboundTarget))
}

func insertPostgresMessage(ctx context.Context, tx *sql.Tx, actor Actor, room Room, input SendMessageRequest) (Message, bool, error) {
	messageTime := input.MessageTime
	if messageTime.IsZero() {
		messageTime = time.Now().UTC()
	}
	messageType := strings.TrimSpace(input.MessageType)
	if messageType == "" {
		messageType = "text"
	}
	message, err := scanMessage(tx.QueryRowContext(ctx, `
		INSERT INTO messages (
			room_id, source_actor_id, external_message_id, sender_id,
			sender_name, message_time, message_type, body
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (room_id, source_actor_id, external_message_id) DO NOTHING
		RETURNING id, room_id, source_actor_id, external_message_id, sender_id,
			sender_name, message_time, message_type, body, created_at
	`, room.ID, actor.ID, strings.TrimSpace(input.ExternalMessageID), strings.TrimSpace(input.SenderID),
		strings.TrimSpace(input.SenderName), messageTime.UTC(), messageType, cloneJSON(input.Body)))
	if err == nil {
		return message, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	existing, err := scanMessage(tx.QueryRowContext(ctx, `
		SELECT id, room_id, source_actor_id, external_message_id, sender_id,
			sender_name, message_time, message_type, body, created_at
		FROM messages
		WHERE room_id = $1 AND source_actor_id = $2 AND external_message_id = $3
	`, room.ID, actor.ID, strings.TrimSpace(input.ExternalMessageID)))
	if err != nil {
		return Message{}, false, err
	}
	return existing, false, nil
}

func enqueuePostgresMessageReceived(ctx context.Context, tx *sql.Tx, room Room, message Message) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM actors
			WHERE kind = $1
				AND enabled = TRUE
				AND permissions ? $2
			UNION
			SELECT actors.id
			FROM actor_subscriptions
			JOIN actors ON actors.id = actor_subscriptions.actor_id
			WHERE actor_subscriptions.room_id = $3
				AND actors.kind = $1
				AND actors.enabled = TRUE
				AND actor_subscriptions.event_types ? $4
		`, ActorKindAIAgent, PermissionSubscribeAll, room.ID, EventMessageReceived)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var actorIDs []int64
	for rows.Next() {
		var actorID int64
		if err := rows.Scan(&actorID); err != nil {
			return err
		}
		actorIDs = append(actorIDs, actorID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, actorID := range actorIDs {
		token := randomToken()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capability_tokens (
				token, actor_id, room_id, action, message_id, uses_remaining, expires_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, token, actorID, room.ID, CapabilityReply, message.ID, 1, time.Now().UTC().Add(time.Hour)); err != nil {
			return err
		}
		if err := enqueuePostgresInbox(ctx, tx, actorID, EventMessageReceived, ResourceMessage, message.ID); err != nil {
			return err
		}
	}
	return nil
}

func enqueuePostgresInbox(ctx context.Context, tx *sql.Tx, actorID int64, eventType string, resourceType string, resourceID int64) error {
	if actorID <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO actor_inbox (actor_id, event_type, resource_type, resource_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (actor_id, event_type, resource_type, resource_id) DO NOTHING
	`, actorID, eventType, resourceType, resourceID)
	return err
}

func getCapabilityTokenForUpdate(ctx context.Context, tx *sql.Tx, token string) (CapabilityToken, error) {
	return scanCapabilityToken(tx.QueryRowContext(ctx, `
		SELECT token, actor_id, room_id, action, message_id, actor_task_id,
			uses_remaining, expires_at, created_at
		FROM capability_tokens
		WHERE token = $1
		FOR UPDATE
	`, token))
}

func getPostgresRoomByID(ctx context.Context, q queryer, roomID int64) (Room, error) {
	return scanRoom(q.QueryRowContext(ctx, `
		SELECT id, tenant_id, channel, provider_account_id, external_room_id,
			room_type, display_name, outbound_target, created_at, updated_at
		FROM rooms
		WHERE id = $1
	`, roomID))
}

func getPostgresMessageByID(ctx context.Context, q queryer, messageID int64) (Message, error) {
	return scanMessage(q.QueryRowContext(ctx, `
		SELECT id, room_id, source_actor_id, external_message_id, sender_id,
			sender_name, message_time, message_type, body, created_at
		FROM messages
		WHERE id = $1
	`, messageID))
}

func getPostgresTaskByID(ctx context.Context, q queryer, taskID int64) (ActorTask, error) {
	return scanActorTask(q.QueryRowContext(ctx, `
		SELECT id, room_id, requester_actor_id, assignee_actor_id, task_type,
			payload, status, created_at, acked_at
		FROM actor_tasks
		WHERE id = $1
	`, taskID))
}

func getPostgresTaskForUpdate(ctx context.Context, tx *sql.Tx, taskID int64) (ActorTask, error) {
	return scanActorTask(tx.QueryRowContext(ctx, `
		SELECT id, room_id, requester_actor_id, assignee_actor_id, task_type,
			payload, status, created_at, acked_at
		FROM actor_tasks
		WHERE id = $1
		FOR UPDATE
	`, taskID))
}

func findPostgresTerminalForRoom(ctx context.Context, tx *sql.Tx, room Room) (Actor, error) {
	actor, err := scanActor(tx.QueryRowContext(ctx, `
		SELECT id, actor_key, kind, name, enabled, channel, provider_account_id, permissions, created_at, updated_at
		FROM actors
		WHERE enabled = TRUE
			AND kind = $1
			AND channel = $2
			AND provider_account_id = $3
		ORDER BY id ASC
		LIMIT 1
	`, ActorKindTerminal, room.Channel, room.ProviderAccountID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, fmt.Errorf("terminal actor not found for room")
		}
		return Actor{}, err
	}
	return actor, nil
}

func getReplyToken(ctx context.Context, q queryer, actorID int64, messageID int64) (string, error) {
	var token string
	err := q.QueryRowContext(ctx, `
		SELECT token
		FROM capability_tokens
		WHERE actor_id = $1 AND message_id = $2 AND action = $3
		ORDER BY created_at DESC
		LIMIT 1
	`, actorID, messageID, CapabilityReply).Scan(&token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return token, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
