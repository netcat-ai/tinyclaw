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

func (s *Store) MarkMessagesDone(ctx context.Context, seqs []int64) error {
	defer dbTimer("mark_messages_done")()

	if len(seqs) == 0 {
		return fmt.Errorf("mark messages done: seqs is empty")
	}

	if _, err := s.db.ExecContext(
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
