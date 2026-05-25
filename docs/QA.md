# TinyClaw QA

## Message Has No Reply

When a user reports that TinyClaw accepts a message but no reply appears, locate the break point from downstream to upstream.

1. Check whether `deliveries` has pending output.

```sql
SELECT
  d.id,
  d.status,
  d.created_at,
  d.acked_at,
  r.channel,
  r.channel_room_type,
  r.channel_room_id,
  r.outbound_alias,
  d.payload
FROM deliveries d
JOIN rooms r ON r.id = d.room_id
WHERE d.status = 0
ORDER BY d.id DESC;
```

If pending deliveries exist, TinyClaw already produced outbound work. Continue with the phone-side delivery path:

- Confirm MobileClaw polling is enabled and using the expected `baseUrl`, `channels`, and `CLAWMAN_API_TOKEN`.
- Confirm the phone can fetch `GET /api/deliveries?channels=<channels>`.
- Check MobileClaw local pending queue; a local pending item blocks new polling until it is sent, acked, or dropped.
- Check the accessibility send flow and whether `POST /api/deliveries/{id}/ack` succeeds after sending.

2. If no pending delivery exists, check whether the inbound message created or advanced an Agent Session.

```sql
SELECT
  m.id,
  m.room_id,
  m.source_message_id,
  m.sender_id,
  m.payload,
  m.skipped,
  m.created_at
FROM messages m
ORDER BY m.id DESC
LIMIT 20;
```

```sql
SELECT
  s.id,
  s.room_id,
  s.agent_key,
  s.enabled,
  s.trigger_message_id,
  s.last_processed_message_id,
  s.codex_session_id,
  s.lock_owner,
  s.lock_expires_at,
  s.updated_at
FROM agent_sessions s
ORDER BY s.updated_at DESC
LIMIT 20;
```

Use these checks to separate the common cases:

- No matching `messages` row: the Channel Adapter did not submit the message or submitted it to the wrong Room.
- Message exists but `trigger_message_id` did not advance: trigger policy did not match, the Agent Session is disabled, the message was duplicate/skipped, or command handling suppressed normal agent trigger.
- `trigger_message_id > last_processed_message_id`: the scheduler has pending agent work; check locks, runner logs, and process health.
- `last_processed_message_id` reached `trigger_message_id` but no delivery exists: the runner produced empty output or delivery creation failed.

3. If the Agent Session was triggered, inspect runner failures.

- Check clawman logs for `claim agent run failed`, `codex exec failed`, `complete agent run failed`, and `fail agent run failed`.
- For Codex runner failures, verify Codex auth, model availability, network/DNS, `CODEX_WORKDIR`, and stale `codex_session_id` recovery.
- A runner failure should create an `agent_failure` delivery. If it does not, debug `FailAgentRun` and delivery insertion first.
