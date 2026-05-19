# Use Append-Only Room Messages

TinyClaw stores inbound messages as append-only room facts. Messages do not store invocation ownership. A message can only be excluded from agent context with `messages.skipped = true`.

Invocation execution boundaries live on `invocations`:

- `trigger_message_id` records the message that created the invocation.
- `start_message_id` records the latest non-skipped room message visible when execution starts.
- `last_seen_message_id` records the latest room message the running agent has explicitly read.

**Considered Options**

- Store `messages.dispatch_state` as waiting/skipped/invocation-id sentinel values.
- Store nullable `messages.invocation_id`.
- Keep messages append-only and move execution cursors to invocations.

**Consequences**

- Prompt boundaries are explicit and reproducible with `start_message_id`.
- Running agents can see user follow-up messages by explicitly reading messages after `last_seen_message_id`.
- Storage does not mutate old messages when an invocation is created or running.
- First version does not persist `input_snapshot`, `output_snapshot`, or `invocation_observations`; use structured logs and deliveries for debugging.
