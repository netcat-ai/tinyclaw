CREATE TABLE IF NOT EXISTS actors (
	id BIGSERIAL PRIMARY KEY,
	actor_key TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL,
	name TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	channel TEXT NOT NULL DEFAULT '',
	provider_account_id TEXT NOT NULL DEFAULT '',
	permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	CHECK (kind IN ('terminal', 'aiagent', 'admin'))
);

CREATE TABLE IF NOT EXISTS rooms (
	id BIGSERIAL PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	channel TEXT NOT NULL,
	provider_account_id TEXT NOT NULL,
	external_room_id TEXT NOT NULL,
	room_type TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	outbound_target TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (tenant_id, channel, provider_account_id, external_room_id),
	CHECK (room_type IN ('direct', 'group'))
);

CREATE TABLE IF NOT EXISTS messages (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	source_actor_id BIGINT NOT NULL REFERENCES actors(id),
	external_message_id TEXT NOT NULL,
	sender_id TEXT NOT NULL,
	sender_name TEXT NOT NULL DEFAULT '',
	message_time TIMESTAMPTZ NOT NULL,
	message_type TEXT NOT NULL,
	body JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (room_id, source_actor_id, external_message_id)
);

CREATE TABLE IF NOT EXISTS actor_subscriptions (
	id BIGSERIAL PRIMARY KEY,
	actor_id BIGINT NOT NULL REFERENCES actors(id),
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	event_types JSONB NOT NULL DEFAULT '["message.received"]'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (actor_id, room_id)
);

CREATE TABLE IF NOT EXISTS actor_inbox (
	id BIGSERIAL PRIMARY KEY,
	actor_id BIGINT NOT NULL REFERENCES actors(id),
	event_type TEXT NOT NULL,
	resource_type TEXT NOT NULL,
	resource_id BIGINT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	acked_at TIMESTAMPTZ,
	UNIQUE (actor_id, event_type, resource_type, resource_id)
);

CREATE TABLE IF NOT EXISTS actor_tasks (
	id BIGSERIAL PRIMARY KEY,
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	requester_actor_id BIGINT REFERENCES actors(id),
	assignee_actor_id BIGINT NOT NULL REFERENCES actors(id),
	task_type TEXT NOT NULL,
	payload JSONB NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	acked_at TIMESTAMPTZ,
	CHECK (status IN ('pending', 'acked', 'failed'))
);

CREATE TABLE IF NOT EXISTS capability_tokens (
	token TEXT PRIMARY KEY,
	actor_id BIGINT NOT NULL REFERENCES actors(id),
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	action TEXT NOT NULL,
	message_id BIGINT REFERENCES messages(id),
	actor_task_id BIGINT REFERENCES actor_tasks(id),
	uses_remaining INTEGER NOT NULL DEFAULT 1,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	CHECK (action IN ('reply', 'send_message', 'ack_task')),
	CHECK (uses_remaining >= 0)
);

CREATE INDEX IF NOT EXISTS idx_actors_kind_enabled ON actors (kind, enabled);
CREATE INDEX IF NOT EXISTS idx_actors_channel_account ON actors (channel, provider_account_id);
CREATE INDEX IF NOT EXISTS idx_rooms_channel_identity ON rooms (tenant_id, channel, provider_account_id, external_room_id);
CREATE INDEX IF NOT EXISTS idx_messages_room_id_id_desc ON messages (room_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_source_actor_id_id ON messages (source_actor_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_room_sender_id ON messages (room_id, sender_id);
CREATE INDEX IF NOT EXISTS idx_actor_subscriptions_room_id ON actor_subscriptions (room_id);
CREATE INDEX IF NOT EXISTS idx_actor_subscriptions_actor_id ON actor_subscriptions (actor_id);
CREATE INDEX IF NOT EXISTS idx_actor_inbox_actor_id_id ON actor_inbox (actor_id, id);
CREATE INDEX IF NOT EXISTS idx_actor_inbox_actor_unacked_id ON actor_inbox (actor_id, id) WHERE acked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_actor_inbox_resource ON actor_inbox (resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_actor_tasks_assignee_status_id ON actor_tasks (assignee_actor_id, status, id);
CREATE INDEX IF NOT EXISTS idx_actor_tasks_requester_id ON actor_tasks (requester_actor_id, id);
CREATE INDEX IF NOT EXISTS idx_actor_tasks_room_id_id ON actor_tasks (room_id, id);
CREATE INDEX IF NOT EXISTS idx_capability_tokens_actor_room ON capability_tokens (actor_id, room_id);
CREATE INDEX IF NOT EXISTS idx_capability_tokens_expires_at ON capability_tokens (expires_at);
