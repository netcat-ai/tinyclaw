package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	wecomCursorSource      = "wecom_archive"
	defaultDeliveryLease   = time.Minute
	defaultDeliveryBackoff = 5 * time.Second
)

type Store struct {
	db *sql.DB
}

type InboundMessageRecord struct {
	ID            string
	TenantID      string
	RoomID        string
	PlatformMsgID string
	SenderID      string
	SenderName    string
	Content       string
	RawPayload    string
	CreatedAt     time.Time
}

type OutboundMessageRecord struct {
	ID         string
	TenantID   string
	RoomID     string
	Content    string
	TargetName string
	CreatedAt  time.Time
}

type Delivery struct {
	ID           int64
	MessageID    string
	RoomID       string
	TargetName   string
	Content      string
	AttemptCount int
}

func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) InitSchema(ctx context.Context) error {
	statements := []string{
		`
		CREATE TABLE IF NOT EXISTS ingest_cursors (
			source TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			cursor BIGINT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (source, tenant_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			room_id TEXT NOT NULL,
			platform_msg_id TEXT UNIQUE,
			direction TEXT NOT NULL,
			sender_id TEXT,
			sender_name TEXT,
			target_name TEXT,
			content TEXT NOT NULL,
			raw_payload TEXT,
			status TEXT NOT NULL DEFAULT 'completed',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS outbox_deliveries (
			id BIGSERIAL PRIMARY KEY,
			message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			room_id TEXT NOT NULL,
			target_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_error TEXT,
			sent_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_outbox_deliveries_ready
		ON outbox_deliveries (status, available_at, id)
		`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}

func (s *Store) GetCursor(ctx context.Context, source, tenantID string) (int64, error) {
	var cursor int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT cursor FROM ingest_cursors WHERE source = $1 AND tenant_id = $2`,
		source,
		tenantID,
	).Scan(&cursor)
	if err == nil {
		return cursor, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return 0, fmt.Errorf("get cursor: %w", err)
}

func (s *Store) SetCursor(ctx context.Context, source, tenantID string, cursor int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO ingest_cursors (source, tenant_id, cursor, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (source, tenant_id)
		DO UPDATE SET cursor = EXCLUDED.cursor, updated_at = NOW()
		`,
		source,
		tenantID,
		cursor,
	)
	if err != nil {
		return fmt.Errorf("set cursor: %w", err)
	}
	return nil
}

func (s *Store) StoreConversation(ctx context.Context, inbound InboundMessageRecord, outbound OutboundMessageRecord) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var insertedID string
	err = tx.QueryRowContext(
		ctx,
		`
		INSERT INTO messages (
			id, tenant_id, room_id, platform_msg_id, direction, sender_id, sender_name, content, raw_payload, created_at
		)
		VALUES ($1, $2, $3, $4, 'inbound', $5, $6, $7, $8, $9)
		ON CONFLICT (platform_msg_id) DO NOTHING
		RETURNING id
		`,
		inbound.ID,
		inbound.TenantID,
		inbound.RoomID,
		inbound.PlatformMsgID,
		inbound.SenderID,
		inbound.SenderName,
		inbound.Content,
		inbound.RawPayload,
		inbound.CreatedAt,
	).Scan(&insertedID)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("insert inbound message: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`
		INSERT INTO messages (
			id, tenant_id, room_id, direction, target_name, content, created_at
		)
		VALUES ($1, $2, $3, 'outbound', $4, $5, $6)
		`,
		outbound.ID,
		outbound.TenantID,
		outbound.RoomID,
		outbound.TargetName,
		outbound.Content,
		outbound.CreatedAt,
	); err != nil {
		return false, fmt.Errorf("insert outbound message: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`
		INSERT INTO outbox_deliveries (
			message_id, room_id, target_name, status, available_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pending', NOW(), NOW(), NOW())
		`,
		outbound.ID,
		outbound.RoomID,
		outbound.TargetName,
	); err != nil {
		return false, fmt.Errorf("insert outbox delivery: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}
	return true, nil
}

func (s *Store) ClaimNextDelivery(ctx context.Context, lease time.Duration) (*Delivery, error) {
	if lease <= 0 {
		lease = defaultDeliveryLease
	}

	row := s.db.QueryRowContext(
		ctx,
		`
		WITH next_delivery AS (
			SELECT id
			FROM outbox_deliveries
			WHERE status IN ('pending', 'retry', 'sending')
			  AND available_at <= NOW()
			ORDER BY available_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE outbox_deliveries AS o
		SET status = 'sending',
			attempt_count = o.attempt_count + 1,
			available_at = NOW() + ($1 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM next_delivery, messages AS m
		WHERE o.id = next_delivery.id
		  AND m.id = o.message_id
		RETURNING o.id, o.message_id, o.room_id, o.target_name, m.content, o.attempt_count
		`,
		int(lease.Seconds()),
	)

	var d Delivery
	err := row.Scan(&d.ID, &d.MessageID, &d.RoomID, &d.TargetName, &d.Content, &d.AttemptCount)
	if err == nil {
		return &d, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return nil, fmt.Errorf("claim delivery: %w", err)
}

func (s *Store) MarkDeliverySent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`
		UPDATE outbox_deliveries
		SET status = 'sent', sent_at = NOW(), updated_at = NOW()
		WHERE id = $1
		`,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark delivery sent: %w", err)
	}
	return nil
}

func (s *Store) MarkDeliveryRetry(ctx context.Context, id int64, backoff time.Duration, errText string) error {
	if backoff <= 0 {
		backoff = defaultDeliveryBackoff
	}
	_, err := s.db.ExecContext(
		ctx,
		`
		UPDATE outbox_deliveries
		SET status = 'retry',
			available_at = NOW() + ($2 * INTERVAL '1 second'),
			last_error = $3,
			updated_at = NOW()
		WHERE id = $1
		`,
		id,
		int(backoff.Seconds()),
		errText,
	)
	if err != nil {
		return fmt.Errorf("mark delivery retry: %w", err)
	}
	return nil
}

func (s *Store) MarkDeliveryFailed(ctx context.Context, id int64, errText string) error {
	_, err := s.db.ExecContext(
		ctx,
		`
		UPDATE outbox_deliveries
		SET status = 'failed', last_error = $2, updated_at = NOW()
		WHERE id = $1
		`,
		id,
		errText,
	)
	if err != nil {
		return fmt.Errorf("mark delivery failed: %w", err)
	}
	return nil
}
