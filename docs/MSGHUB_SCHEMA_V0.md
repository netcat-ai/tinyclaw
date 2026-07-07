# Msghub Schema v0

> Status: draft.
> Goal: create a separate `msghub` database while reusing the proven design ideas from Clawman's Core Model.

## 1. Boundary

`msghub` uses its own database. It must not mutate or depend on the current Clawman database.

Reuse means copying the ideas that worked, not carrying compatibility fields:

```text
Clawman api_clients -> authenticated protocol Actors
Clawman rooms       -> stable message carrier Rooms
Clawman messages    -> append-only message facts with idempotency
Clawman deliveries  -> durable Actor Tasks / outbox
Clawman token table -> scoped capability tokens
```

Do not copy Clawman agent runtime tables or fields into `msghub`:

```text
agent_sessions
agents
memory_items
memory_write_jobs
memory_change_audit
memory_capability_tokens
agent_session_id
source_message_from_id
source_message_to_id
prompt
```

`msghub` is a mailbox-shaped actor broker:

- `rooms` carry all message timelines, including direct chats.
- `messages` are append-only raw message facts inside one Room.
- `actors` are protocol clients that can pull inbox items and send messages.
- `actor_subscriptions` records which Actor subscribes to which Room.
- `actor_inbox` stores per-Actor mailbox items.
- `actor_tasks` stores executable work assigned to an Actor.
- `capability_tokens` stores iLink-like scoped authority.

## 2. Core Tables

### 2.1 actors

Actors reuse Clawman's API client idea: store identity, hashed bearer token, enabled state, and permissions.

```sql
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
```

Rules:

- `terminal`: a real endpoint adapter, such as Linux WeChat.
- `aiagent`: an AI consumer, such as future Clawman-as-agent.
- `admin`: management client.
- `channel` and `provider_account_id` are mainly for terminal Actors.
- Actor is not the same thing as the user who sent a message in WeChat.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_actors_kind_enabled ON actors (kind, enabled);
CREATE INDEX IF NOT EXISTS idx_actors_channel_account ON actors (channel, provider_account_id);
```

### 2.2 rooms

Rooms reuse Clawman's Room idea: a registered external conversation identity that owns one message timeline.

```sql
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
```

Rules:

- Every Message belongs to exactly one Room.
- Direct chats are Rooms with `room_type = 'direct'`.
- Group chats are Rooms with `room_type = 'group'`.
- `provider_account_id` scopes Rooms for multi-account terminals.
- `external_room_id` is the provider conversation id.
- `outbound_target` is the provider-facing value a terminal Actor needs for sending.
- Room does not own prompt, trigger policy, memory, or runner state.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_rooms_channel_identity ON rooms (tenant_id, channel, provider_account_id, external_room_id);
```

### 2.3 messages

Messages reuse Clawman's strongest idea: append-only provider facts with a Room-scoped idempotency key.

```sql
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
```

Rules:

- `room_id + source_actor_id + external_message_id` is the idempotency key.
- `sender_id` and `sender_name` are the v0 sender snapshot.
- `message_type + body` keeps Clawman's typed payload idea without carrying old wire field names.
- Multi-part iLink-like messages use `message_type = 'mixed'` and store `body.parts`.
- AI send intent does not become a Message until the terminal sends it or the provider self-echoes it.
- Do not add `direction` in v0; infer it from source Actor and Room context when needed.
- Do not add `raw_payload` in v0; provider-specific data belongs in `body`.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_messages_room_id_id_desc ON messages (room_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_source_actor_id_id ON messages (source_actor_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_room_sender_id ON messages (room_id, sender_id);
```

### 2.4 actor_subscriptions

Actor subscriptions decide which Room events are delivered to an Actor inbox.

```sql
CREATE TABLE IF NOT EXISTS actor_subscriptions (
	id BIGSERIAL PRIMARY KEY,
	actor_id BIGINT NOT NULL REFERENCES actors(id),
	room_id BIGINT NOT NULL REFERENCES rooms(id),
	event_types JSONB NOT NULL DEFAULT '["message.received"]'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (actor_id, room_id)
);
```

Rules:

- `aiagent` Actors subscribe to Rooms they should observe.
- Terminal Actors do not need a Room subscription to receive assigned tasks.
- Event filtering happens when inserting `actor_inbox` rows.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_actor_subscriptions_room_id ON actor_subscriptions (room_id);
CREATE INDEX IF NOT EXISTS idx_actor_subscriptions_actor_id ON actor_subscriptions (actor_id);
```

### 2.5 actor_inbox

`actor_inbox` is the per-Actor mailbox. It stores delivery state, not business state.

```sql
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
```

Rules:

- `resource_type = 'message'` points to `messages.id`.
- `resource_type = 'actor_task'` points to `actor_tasks.id`.
- `acked_at` only means the Actor acknowledged this inbox item.
- Task success or failure belongs to `actor_tasks.status`, not `actor_inbox`.

Initial event types:

```text
message.received
message.send_requested
message.sent
message.failed
room.updated
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_actor_inbox_actor_id_id ON actor_inbox (actor_id, id);
CREATE INDEX IF NOT EXISTS idx_actor_inbox_actor_unacked_id ON actor_inbox (actor_id, id) WHERE acked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_actor_inbox_resource ON actor_inbox (resource_type, resource_id);
```

### 2.6 actor_tasks

Actor Tasks reuse Clawman's Delivery idea: a durable outbox item with status and ack.

```sql
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
```

Rules:

- First task type is `send_message`.
- `requester_actor_id` is usually an `aiagent`.
- `assignee_actor_id` is usually a `terminal`.
- `payload` carries task body, target details, optional reply target, and optional error after failure.
- Do not add lock, retry, or completion columns in v0. Revisit only after terminal execution requires them.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_actor_tasks_assignee_status_id ON actor_tasks (assignee_actor_id, status, id);
CREATE INDEX IF NOT EXISTS idx_actor_tasks_requester_id ON actor_tasks (requester_actor_id, id);
CREATE INDEX IF NOT EXISTS idx_actor_tasks_room_id_id ON actor_tasks (room_id, id);
```

### 2.7 capability_tokens

Capability tokens store iLink-like scoped authority with expiry.

V0 stores the token value because `getupdates` must be able to replay the same pending inbox item to a polling Actor. A later version can replace this with signed self-contained tokens or encrypted token storage.

```sql
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
```

Rules:

- `action = 'reply'` allows an aiagent Actor to create a `send_message` task for the scoped Room.
- `action = 'ack_task'` allows a terminal Actor to acknowledge or fail a specific task.
- `message_id` is the message that created the reply capability, when applicable.
- `actor_task_id` is the task controlled by the capability, when applicable.
- Validation checks actor, Room, action, expiry, and remaining uses.

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_capability_tokens_actor_room ON capability_tokens (actor_id, room_id);
CREATE INDEX IF NOT EXISTS idx_capability_tokens_expires_at ON capability_tokens (expires_at);
```

## 3. Deferred Tables

### room_participants

Do not add in v0.

V0 stores sender identity snapshots directly on `messages`:

```text
sender_id
sender_name
```

Add `room_participants` later only when these become real requirements:

- list participants in a Room;
- track current display names;
- distinguish self/bot/member/admin;
- address a specific member;
- merge identities across Rooms.

### media_objects

Do not add in v0.

V0 stores media metadata inside:

```text
messages.body
actor_tasks.payload
```

Add `media_objects` later only when these become real requirements:

- upload media before send;
- reuse media across messages or tasks;
- expire or garbage collect media;
- store encrypted CDN metadata;
- serve media through scoped URLs.

## 4. Write Flows

### 4.1 Terminal Actor Appends an Inbound Message

```text
terminal actor sendmessage
  -> upsert/find room
  -> insert messages row using (room_id, source_actor_id, external_message_id)
  -> find actor_subscriptions for room
  -> insert actor_inbox rows with event_type = message.received
  -> create capability_tokens for subscribed aiagent actors
```

### 4.2 Aiagent Actor Requests a Reply

```text
aiagent actor sendmessage(context_token)
  -> validate capability_tokens(action = reply)
  -> insert actor_tasks row with task_type = send_message
  -> insert actor_inbox row for terminal actor with event_type = message.send_requested
```

### 4.3 Terminal Actor Executes a Send Task

```text
terminal actor getupdates
  -> reads actor_inbox rows
  -> executes actor_task
  -> sets actor_tasks.status = acked and acked_at = now() on success
  -> sets actor_tasks.status = failed and stores error in payload on failure
  -> inserts actor_inbox status rows for requester actor
```

## 5. Implementation Strategy

V0 creates a new `msghub` database or schema. It does not mutate the current Clawman database.

Steps:

1. Create the `msghub` database/schema.
2. Create `actors`, `rooms`, `messages`, `actor_subscriptions`, `actor_inbox`, `actor_tasks`, and `capability_tokens`.
3. Copy Clawman's repository patterns for room upsert, message idempotency, JSON body storage, auth, and outbox polling.
4. Do not import existing Clawman data unless a separate migration is explicitly requested.
5. Keep Clawman runtime tables and fields outside `msghub`.

This preserves the design work already done in Clawman without coupling `msghub` to Clawman's database or API compatibility constraints.
