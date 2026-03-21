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
	statusIgnored  = "ignored"
	statusBuffered = "buffered"
	statusPending  = "pending"
	statusDone     = "done"

	defaultDeliveryLease   = time.Minute
	defaultDeliveryBackoff = 5 * time.Second
)

type Store struct {
	db *sql.DB
}

type MessageRecord struct {
	Seq       int64
	TenantID  string
	MsgID     string
	RoomID    string
	FromID    string
	FromName  string
	Payload   string
	Status    string
	MsgTime   time.Time
	CreatedAt time.Time
}

type DeliveryRecord struct {
	TenantID   string
	RoomID     string
	TargetName string
	Content    string
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
		CREATE TABLE IF NOT EXISTS messages (
			seq BIGINT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			msgid TEXT,
			room_id TEXT,
			from_id TEXT,
			from_name TEXT,
			payload TEXT NOT NULL,
			status TEXT NOT NULL,
			msg_time TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CHECK (status IN ('ignored', 'buffered', 'pending', 'done'))
		)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_messages_room_seq
		ON messages (tenant_id, room_id, seq)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_messages_pending_by_room
		ON messages (tenant_id, status, room_id, seq)
		WHERE status = 'pending'
		`,
		`
		CREATE TABLE IF NOT EXISTS outbox_deliveries (
			id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			room_id TEXT NOT NULL,
			target_name TEXT NOT NULL,
			content TEXT NOT NULL,
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

func (s *Store) GetMaxSeq(ctx context.Context, tenantID string) (int64, bool, error) {
	defer dbTimer("get_max_seq")()

	var maxSeq sql.NullInt64
	err := s.db.QueryRowContext(
		ctx,
		`
		SELECT MAX(seq)
		FROM messages
		WHERE tenant_id = $1
		`,
		tenantID,
	).Scan(&maxSeq)
	if err != nil {
		return 0, false, fmt.Errorf("get max seq: %w", err)
	}

	if !maxSeq.Valid {
		return 0, false, nil
	}
	return maxSeq.Int64, true, nil
}

func (s *Store) StoreMessage(ctx context.Context, record MessageRecord, promoteBuffered bool) (bool, error) {
	defer dbTimer("store_message")()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	inserted, err := insertMessageTx(ctx, tx, record)
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}

	if promoteBuffered && record.RoomID != "" {
		if _, err := tx.ExecContext(
			ctx,
			`
			UPDATE messages
			SET status = $4
			WHERE tenant_id = $1
			  AND room_id = $2
			  AND seq IS NOT NULL
			  AND status = $3
			`,
			record.TenantID,
			record.RoomID,
			statusBuffered,
			statusPending,
		); err != nil {
			return false, fmt.Errorf("promote buffered messages: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}
	return true, nil
}

func insertMessageTx(ctx context.Context, tx *sql.Tx, record MessageRecord) (bool, error) {
	var insertedSeq int64
	err := tx.QueryRowContext(
		ctx,
		`
		INSERT INTO messages (
			seq,
			tenant_id,
			msgid,
			room_id,
			from_id,
			from_name,
			payload,
			status,
			msg_time,
			created_at
		)
		VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			$7,
			$8,
			$9,
			$10
		)
		ON CONFLICT (seq) DO NOTHING
		RETURNING seq
		`,
		record.Seq,
		record.TenantID,
		nullIfEmpty(record.MsgID),
		nullIfEmpty(record.RoomID),
		nullIfEmpty(record.FromID),
		nullIfEmpty(record.FromName),
		record.Payload,
		record.Status,
		nullTime(record.MsgTime),
		record.CreatedAt,
	).Scan(&insertedSeq)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("insert message seq=%d: %w", record.Seq, err)
	}
}

func (s *Store) ListPendingRooms(ctx context.Context, tenantID string) ([]string, error) {
	defer dbTimer("list_pending_rooms")()

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT room_id
		FROM messages
		WHERE tenant_id = $1
		  AND status = $2
		  AND room_id IS NOT NULL
		GROUP BY room_id
		ORDER BY MIN(seq)
		`,
		tenantID,
		statusPending,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending rooms: %w", err)
	}
	defer rows.Close()

	var rooms []string
	for rows.Next() {
		var roomID string
		if err := rows.Scan(&roomID); err != nil {
			return nil, fmt.Errorf("scan pending room: %w", err)
		}
		rooms = append(rooms, roomID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending rooms: %w", err)
	}
	return rooms, nil
}

func (s *Store) ListPendingMessages(ctx context.Context, tenantID, roomID string) ([]MessageRecord, error) {
	defer dbTimer("list_pending_messages")()

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT seq, tenant_id, msgid, room_id, from_id, from_name, payload, status, msg_time, created_at
		FROM messages
		WHERE tenant_id = $1
		  AND room_id = $2
		  AND status = $3
		ORDER BY seq
		`,
		tenantID,
		roomID,
		statusPending,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending messages: %w", err)
	}
	defer rows.Close()

	var records []MessageRecord
	for rows.Next() {
		var record MessageRecord
		var msgTime sql.NullTime
		if err := rows.Scan(
			&record.Seq,
			&record.TenantID,
			&record.MsgID,
			&record.RoomID,
			&record.FromID,
			&record.FromName,
			&record.Payload,
			&record.Status,
			&msgTime,
			&record.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending message: %w", err)
		}
		if msgTime.Valid {
			record.MsgTime = msgTime.Time
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending messages: %w", err)
	}
	return records, nil
}

func (s *Store) MarkMessagesDoneAndEnqueueDelivery(ctx context.Context, seqs []int64, delivery DeliveryRecord) error {
	defer dbTimer("mark_done_enqueue_delivery")()

	if len(seqs) == 0 {
		return fmt.Errorf("mark messages done: seqs is empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`
		UPDATE messages
		SET status = $2
		WHERE seq = ANY($1)
		`,
		seqs,
		statusDone,
	); err != nil {
		return fmt.Errorf("mark messages done: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`
		INSERT INTO outbox_deliveries (
			tenant_id,
			room_id,
			target_name,
			content,
			status,
			available_at,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, 'pending', NOW(), NOW(), NOW())
		`,
		delivery.TenantID,
		delivery.RoomID,
		delivery.TargetName,
		delivery.Content,
	); err != nil {
		return fmt.Errorf("insert outbox delivery: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *Store) ClaimNextDelivery(ctx context.Context, lease time.Duration) (*Delivery, error) {
	defer dbTimer("claim_delivery")()
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
		FROM next_delivery
		WHERE o.id = next_delivery.id
		RETURNING o.id, '', o.room_id, o.target_name, o.content, o.attempt_count
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
	defer dbTimer("mark_sent")()
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
	defer dbTimer("mark_retry")()
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
	defer dbTimer("mark_failed")()
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
