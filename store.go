package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	db *sql.DB
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

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) InitSchema(ctx context.Context) error {
	statements := []string{
		`
		CREATE TABLE IF NOT EXISTS core_rooms (
			id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			channel_room_id TEXT NOT NULL,
			channel_room_type TEXT NOT NULL,
			display_name TEXT,
			trigger_policy JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (tenant_id, channel, channel_room_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS core_messages (
			id BIGSERIAL PRIMARY KEY,
			room_id BIGINT NOT NULL REFERENCES core_rooms(id),
			source_message_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			sender_name TEXT,
			payload JSONB NOT NULL,
			message_time TIMESTAMPTZ NOT NULL,
			dispatch_state BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (room_id, source_message_id),
			CHECK (dispatch_state IN (0, 1) OR dispatch_state >= 1000)
		)
		`,
		`
		CREATE SEQUENCE IF NOT EXISTS core_invocations_id_seq START WITH 1000
		`,
		`
		CREATE TABLE IF NOT EXISTS core_invocations (
			id BIGINT PRIMARY KEY DEFAULT nextval('core_invocations_id_seq'),
			room_id BIGINT NOT NULL REFERENCES core_rooms(id),
			status TEXT NOT NULL,
			trigger_message_id BIGINT REFERENCES core_messages(id),
			input_snapshot JSONB NOT NULL,
			output_snapshot JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			CHECK (id >= 1000),
			CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled'))
		)
		`,
		`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_core_invocations_one_active_per_room
		ON core_invocations (room_id)
		WHERE status IN ('queued', 'running')
		`,
		`
		CREATE TABLE IF NOT EXISTS core_deliveries (
			id BIGSERIAL PRIMARY KEY,
			seq BIGSERIAL UNIQUE,
			room_id BIGINT NOT NULL REFERENCES core_rooms(id),
			invocation_id BIGINT NOT NULL REFERENCES core_invocations(id),
			payload JSONB NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			acked_at TIMESTAMPTZ,
			CHECK (status IN ('pending', 'acked', 'failed'))
		)
		`,
		`
		CREATE INDEX IF NOT EXISTS idx_core_deliveries_status_seq
		ON core_deliveries (status, seq)
		`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}
