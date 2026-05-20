CREATE TABLE IF NOT EXISTS rooms (
	id BIGSERIAL PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	channel TEXT NOT NULL,
	channel_room_id TEXT NOT NULL,
	channel_room_type TEXT NOT NULL,
	display_name TEXT,
	outbound_alias TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (tenant_id, channel, channel_room_id)
);

CREATE TABLE IF NOT EXISTS messages (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	source_message_id TEXT NOT NULL,
	sender_id TEXT NOT NULL,
	sender_name TEXT,
	payload JSONB NOT NULL,
	message_time TIMESTAMPTZ NOT NULL,
	skipped BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id, source_message_id)
);

CREATE TABLE IF NOT EXISTS agent_sessions (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_key TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	trigger_policy JSONB,
	trigger_message_id BIGINT REFERENCES messages(id),
	last_processed_message_id BIGINT NOT NULL DEFAULT 0,
	lock_owner TEXT,
	lock_expires_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id, agent_key)
);

CREATE TABLE IF NOT EXISTS deliveries (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_session_id BIGINT REFERENCES agent_sessions(id),
	source_message_after_id BIGINT NOT NULL DEFAULT 0,
	source_message_until_id BIGINT NOT NULL DEFAULT 0,
	payload JSONB NOT NULL,
	status SMALLINT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	acked_at TIMESTAMPTZ
);
