CREATE TABLE IF NOT EXISTS api_clients (
	id BIGSERIAL PRIMARY KEY,
	client_id TEXT NOT NULL UNIQUE,
	client_secret_hash TEXT NOT NULL,
	name TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	permissions JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
	source TEXT NOT NULL,
	sender_id TEXT NOT NULL,
	sender_name TEXT,
	payload JSONB NOT NULL,
	message_time TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id, source_message_id)
);

CREATE TABLE IF NOT EXISTS agent_sessions (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	trigger_policy JSONB,
	pending_trigger_message_id BIGINT REFERENCES messages(id),
	caught_up_message_id BIGINT NOT NULL DEFAULT 0,
	codex_session_id TEXT,
	lock_owner TEXT,
	lock_expires_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id)
);

CREATE TABLE IF NOT EXISTS agents (
	id BIGSERIAL PRIMARY KEY,
	key TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	description TEXT,
	prompt TEXT NOT NULL,
	allowed_tools JSONB NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS deliveries (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_session_id BIGINT REFERENCES agent_sessions(id),
	source_message_from_id BIGINT NOT NULL DEFAULT 0,
	source_message_to_id BIGINT NOT NULL DEFAULT 0,
	payload JSONB NOT NULL,
	status SMALLINT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	acked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS memory_items (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	type TEXT NOT NULL,
	key TEXT NOT NULL,
	content TEXT NOT NULL,
	status TEXT NOT NULL,
	source_message_from_id BIGINT NOT NULL DEFAULT 0,
	source_message_to_id BIGINT NOT NULL DEFAULT 0,
	created_by_agent_session_id BIGINT REFERENCES agent_sessions(id),
	updated_by_agent_session_id BIGINT REFERENCES agent_sessions(id),
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id, type, key)
);

CREATE TABLE IF NOT EXISTS memory_write_jobs (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_session_id BIGINT NOT NULL REFERENCES agent_sessions(id),
	agent_id BIGINT REFERENCES agents(id),
	source_message_from_id BIGINT NOT NULL,
	source_message_to_id BIGINT NOT NULL,
	operation_key TEXT NOT NULL UNIQUE,
	op TEXT NOT NULL,
	type TEXT NOT NULL,
	key TEXT NOT NULL,
	content TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS memory_change_audit (
	id BIGSERIAL PRIMARY KEY,
	memory_item_id BIGINT REFERENCES memory_items(id),
	memory_write_job_id BIGINT REFERENCES memory_write_jobs(id),
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_session_id BIGINT REFERENCES agent_sessions(id),
	action TEXT NOT NULL,
	payload JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS memory_capability_tokens (
	token_hash TEXT PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	agent_session_id BIGINT NOT NULL REFERENCES agent_sessions(id),
	source_message_from_id BIGINT NOT NULL,
	source_message_to_id BIGINT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messages_room_id_id_desc ON messages (room_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_deliveries_status_id ON deliveries (status, id);
CREATE INDEX IF NOT EXISTS idx_deliveries_room_source_message_to_id ON deliveries (room_id, source_message_to_id);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_pending_caught_up ON agent_sessions (pending_trigger_message_id, caught_up_message_id);
CREATE INDEX IF NOT EXISTS idx_memory_items_room_status_updated_at_desc ON memory_items (room_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_write_jobs_status_id ON memory_write_jobs (status, id);
CREATE INDEX IF NOT EXISTS idx_memory_capability_tokens_expires_at ON memory_capability_tokens (expires_at);
