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
	statusSent     = "sent"
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

type Job struct {
	ID             string
	Seq            int64
	BotID          string
	RecipientAlias string
	Message        string
	MaxSeq         int64
	CreatedAt      time.Time
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

	pingCtx, cancel := context.WithTimeout(ctx, dbStartupTimeout)
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
			CHECK (status IN ('ignored', 'buffered', 'pending', 'sent', 'done'))
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
		CREATE INDEX IF NOT EXISTS idx_messages_sent_by_room
		ON messages (tenant_id, status, room_id, seq)
		WHERE status = 'sent'
		`,
		`
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			seq BIGSERIAL UNIQUE,
			bot_id TEXT NOT NULL,
			recipient_alias TEXT NOT NULL,
			message TEXT NOT NULL,
			max_seq BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_jobs_bot_seq
		ON jobs (bot_id, seq)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_jobs_bot_created_at
		ON jobs (bot_id, created_at)
		`,
		`
		CREATE TABLE IF NOT EXISTS wecom_app_clients (
			client_id TEXT PRIMARY KEY,
			bot_id TEXT NOT NULL,
			client_secret TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE
		)
		`,
		`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_wecom_app_clients_bot_id
		ON wecom_app_clients (bot_id)
		`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	if _, err := s.db.ExecContext(
		ctx,
		`
		ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check
		`,
	); err != nil {
		return fmt.Errorf("drop messages status constraint: %w", err)
	}
	if _, err := s.db.ExecContext(
		ctx,
		`
		ALTER TABLE messages
		ADD CONSTRAINT messages_status_check
		CHECK (status IN ('ignored', 'buffered', 'pending', 'sent', 'done'))
		`,
	); err != nil {
		return fmt.Errorf("add messages status constraint: %w", err)
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
	return s.markMessagesStatus(ctx, "mark_messages_done", seqs, statusDone)
}

func (s *Store) MarkMessagesSent(ctx context.Context, seqs []int64) error {
	return s.markMessagesStatus(ctx, "mark_messages_sent", seqs, statusSent)
}

func (s *Store) MarkMessagesPending(ctx context.Context, seqs []int64) error {
	return s.markMessagesStatus(ctx, "mark_messages_pending", seqs, statusPending)
}

func (s *Store) MarkMessagesIgnored(ctx context.Context, seqs []int64) error {
	return s.markMessagesStatus(ctx, "mark_messages_ignored", seqs, statusIgnored)
}

func (s *Store) ResetSentMessages(ctx context.Context) error {
	defer dbTimer("reset_sent_messages")()

	if _, err := s.db.ExecContext(
		ctx,
		`
		UPDATE messages
		SET status = $1
		WHERE status = $2
		`,
		statusPending,
		statusSent,
	); err != nil {
		return fmt.Errorf("reset sent messages: %w", err)
	}
	return nil
}

func (s *Store) markMessagesStatus(ctx context.Context, metricName string, seqs []int64, status string) error {
	defer dbTimer(metricName)()

	if len(seqs) == 0 {
		return fmt.Errorf("mark messages %s: seqs is empty", status)
	}

	if _, err := s.db.ExecContext(
		ctx,
		`
		UPDATE messages
		SET status = $2
		WHERE seq = ANY($1)
		`,
		seqs,
		status,
	); err != nil {
		return fmt.Errorf("mark messages %s: %w", status, err)
	}
	return nil
}

func (s *Store) EnqueueJob(ctx context.Context, botID, recipientAlias, message string, maxSeq int64) (Job, error) {
	defer dbTimer("enqueue_job")()

	job := Job{
		ID:             uuid.NewString(),
		BotID:          botID,
		RecipientAlias: recipientAlias,
		Message:        message,
		MaxSeq:         maxSeq,
		CreatedAt:      time.Now().UTC(),
	}

	err := s.db.QueryRowContext(
		ctx,
		`
		INSERT INTO jobs (
			id,
			bot_id,
			recipient_alias,
			message,
			max_seq,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING seq
		`,
		job.ID,
		job.BotID,
		job.RecipientAlias,
		job.Message,
		job.MaxSeq,
		job.CreatedAt,
	).Scan(&job.Seq)
	if err != nil {
		return Job{}, fmt.Errorf("enqueue job: %w", err)
	}

	return job, nil
}

func (s *Store) GetMaxJobSeq(ctx context.Context, botID string) (int64, error) {
	defer dbTimer("get_max_job_seq")()

	var maxSeq sql.NullInt64
	if err := s.db.QueryRowContext(
		ctx,
		`
		SELECT MAX(seq)
		FROM jobs
		WHERE bot_id = $1
		`,
		botID,
	).Scan(&maxSeq); err != nil {
		return 0, fmt.Errorf("get max job seq: %w", err)
	}
	if !maxSeq.Valid {
		return 0, nil
	}
	return maxSeq.Int64, nil
}

func (s *Store) ListJobsSinceSeq(ctx context.Context, botID string, afterSeq int64, cutoff time.Time) ([]Job, error) {
	defer dbTimer("list_jobs_since_seq")()

	rows, err := s.db.QueryContext(
		ctx,
		`
		SELECT id, seq, bot_id, recipient_alias, message, max_seq, created_at
		FROM jobs
		WHERE bot_id = $1
		  AND seq > $2
		  AND created_at >= $3
		ORDER BY seq ASC
		`,
		botID,
		afterSeq,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("list jobs since seq: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		if err := rows.Scan(
			&job.ID,
			&job.Seq,
			&job.BotID,
			&job.RecipientAlias,
			&job.Message,
			&job.MaxSeq,
			&job.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) AuthenticateAppClient(ctx context.Context, clientID, clientSecret string) (string, bool, error) {
	defer dbTimer("authenticate_app_client")()

	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" {
		return "", false, fmt.Errorf("client id is empty")
	}
	if clientSecret == "" {
		return "", false, fmt.Errorf("client secret is empty")
	}

	var storedSecret string
	var botID string
	var enabled bool
	if err := s.db.QueryRowContext(
		ctx,
		`
		SELECT client_secret, bot_id, enabled
		FROM wecom_app_clients
		WHERE client_id = $1
		`,
		clientID,
	).Scan(&storedSecret, &botID, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("invalid client: %s", clientID)
		}
		return "", false, fmt.Errorf("authenticate app client: %w", err)
	}
	if !enabled {
		return "", false, fmt.Errorf("client disabled: %s", clientID)
	}
	if storedSecret != clientSecret {
		return "", false, fmt.Errorf("invalid secret for client: %s", clientID)
	}
	if strings.TrimSpace(botID) == "" {
		return "", false, fmt.Errorf("app client %s has empty bot id", clientID)
	}
	return botID, true, nil
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
