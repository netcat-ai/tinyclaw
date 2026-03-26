package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	statusIgnored  = "ignored"
	statusBuffered = "buffered"
	statusPending  = "pending"
	statusDone     = "done"

	sendJobStatusQueued    = "queued"
	sendJobStatusClaimed   = "claimed"
	sendJobStatusSucceeded = "succeeded"
	sendJobStatusFailed    = "failed"
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

type SendJob struct {
	ID             string
	RecipientAlias string
	Message        string
	Status         string
	DeviceID       string
	Attempts       int
	LastError      string
	ClaimDeadline  time.Time
	ClaimedAt      time.Time
	CompletedAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
		CREATE TABLE IF NOT EXISTS wecom_send_jobs (
			id TEXT PRIMARY KEY,
			recipient_alias TEXT NOT NULL,
			message TEXT NOT NULL,
			status TEXT NOT NULL,
			device_id TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			claim_deadline TIMESTAMPTZ,
			claimed_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CHECK (status IN ('queued', 'claimed', 'succeeded', 'failed'))
		)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_wecom_send_jobs_claimable
		ON wecom_send_jobs (status, created_at)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_wecom_send_jobs_claim_deadline
		ON wecom_send_jobs (status, claim_deadline)
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

func (s *Store) EnqueueSendJob(ctx context.Context, recipientAlias, message string) (SendJob, error) {
	defer dbTimer("enqueue_send_job")()

	now := time.Now().UTC()
	job := SendJob{
		ID:             uuid.NewString(),
		RecipientAlias: recipientAlias,
		Message:        message,
		Status:         sendJobStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if _, err := s.db.ExecContext(
		ctx,
		`
		INSERT INTO wecom_send_jobs (
			id,
			recipient_alias,
			message,
			status,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		`,
		job.ID,
		job.RecipientAlias,
		job.Message,
		job.Status,
		job.CreatedAt,
		job.UpdatedAt,
	); err != nil {
		return SendJob{}, fmt.Errorf("enqueue send job: %w", err)
	}

	return job, nil
}

func (s *Store) ClaimNextSendJob(ctx context.Context, deviceID string, lease time.Duration) (*SendJob, error) {
	defer dbTimer("claim_send_job")()

	if strings.TrimSpace(deviceID) == "" {
		return nil, fmt.Errorf("claim send job: deviceID is empty")
	}
	if lease <= 0 {
		return nil, fmt.Errorf("claim send job: lease must be positive")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer tx.Rollback()

	var job SendJob
	err = tx.QueryRowContext(
		ctx,
		`
		WITH candidate AS (
			SELECT id
			FROM wecom_send_jobs
			WHERE status = $1
			   OR (status = $2 AND claim_deadline IS NOT NULL AND claim_deadline < NOW())
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE wecom_send_jobs AS jobs
		SET status = $2,
			device_id = $3,
			attempts = jobs.attempts + 1,
			last_error = NULL,
			claimed_at = NOW(),
			claim_deadline = NOW() + ($4 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM candidate
		WHERE jobs.id = candidate.id
		RETURNING
			jobs.id,
			jobs.recipient_alias,
			jobs.message,
			jobs.status,
			COALESCE(jobs.device_id, ''),
			jobs.attempts,
			COALESCE(jobs.last_error, ''),
			COALESCE(jobs.claim_deadline, TIMESTAMPTZ 'epoch'),
			COALESCE(jobs.claimed_at, TIMESTAMPTZ 'epoch'),
			COALESCE(jobs.completed_at, TIMESTAMPTZ 'epoch'),
			jobs.created_at,
			jobs.updated_at
		`,
		sendJobStatusQueued,
		sendJobStatusClaimed,
		deviceID,
		int(lease.Seconds()),
	).Scan(
		&job.ID,
		&job.RecipientAlias,
		&job.Message,
		&job.Status,
		&job.DeviceID,
		&job.Attempts,
		&job.LastError,
		&job.ClaimDeadline,
		&job.ClaimedAt,
		&job.CompletedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	switch {
	case err == nil:
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit claim tx: %w", err)
		}
		return &job, nil
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	default:
		return nil, fmt.Errorf("claim send job: %w", err)
	}
}

func (s *Store) FinishSendJob(ctx context.Context, jobID, deviceID, status, lastError string) (*SendJob, error) {
	defer dbTimer("finish_send_job")()

	if status != sendJobStatusSucceeded && status != sendJobStatusFailed {
		return nil, fmt.Errorf("finish send job: invalid status %q", status)
	}

	var job SendJob
	err := s.db.QueryRowContext(
		ctx,
		`
		UPDATE wecom_send_jobs
		SET status = $3,
			last_error = NULLIF($4, ''),
			claim_deadline = NULL,
			completed_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		  AND device_id = $2
		  AND status = $5
		RETURNING
			id,
			recipient_alias,
			message,
			status,
			COALESCE(device_id, ''),
			attempts,
			COALESCE(last_error, ''),
			COALESCE(claim_deadline, TIMESTAMPTZ 'epoch'),
			COALESCE(claimed_at, TIMESTAMPTZ 'epoch'),
			COALESCE(completed_at, TIMESTAMPTZ 'epoch'),
			created_at,
			updated_at
		`,
		jobID,
		deviceID,
		status,
		lastError,
		sendJobStatusClaimed,
	).Scan(
		&job.ID,
		&job.RecipientAlias,
		&job.Message,
		&job.Status,
		&job.DeviceID,
		&job.Attempts,
		&job.LastError,
		&job.ClaimDeadline,
		&job.ClaimedAt,
		&job.CompletedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	switch {
	case err == nil:
		return &job, nil
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	default:
		return nil, fmt.Errorf("finish send job: %w", err)
	}
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
