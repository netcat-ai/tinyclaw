# Use Append-Only Room Messages

TinyClaw stores messages as append-only raw facts that have entered one Room through its channel. Messages do not store invocation ownership or global visibility state.

Agent execution boundaries live on the Room's default `agent_sessions` row:

- `pending_trigger_message_id` records the latest triggering Message boundary.
- `caught_up_message_id` records the Room Message boundary the Agent Session has caught up to.
- Runner context is chosen from bounded recent Messages, Room Memory, and tools; it does not require replaying every Message between the two boundaries.

**Considered Options**

- Store `messages.dispatch_state` as waiting/ignored/invocation-id sentinel values.
- Store nullable `messages.invocation_id`.
- Keep messages append-only and move execution cursors to Agent Session state.

**Consequences**

- Message history remains a raw audit timeline.
- Prompt construction can evolve without mutating Message rows.
- Storage does not mutate old messages when an invocation is created or running.
- First version does not persist prompt snapshots or run-step snapshots; use structured logs, deliveries, and memory write jobs for debugging.
