# Use Sentinel Dispatch State on Messages

TinyClaw stores message assignment state in `messages.dispatch_state`: `0` means waiting, `1` means skipped, and values `>= 1000` refer by convention to real rows in `invocations`. We intentionally do not use a database foreign key for this field, because the first execution model prioritizes a compact dispatch query and reserves sentinel values for non-invocation states. Real invocation ids must start at `1000`, and database checks should reject any value other than `0`, `1`, or `>= 1000`.

**Considered Options**

- Nullable `messages.invocation_id` plus explicit `ignored_at` / `ignore_reason`.
- Per-room ignored invocation rows with a real foreign key.
- A global reserved ignored invocation row with `id = 1`.
- A `messages.invocation_id` field overloaded with sentinel values.

**Consequences**

- Application code must treat `0` and `1` as reserved dispatch states, not real invocation ids.
- Queries that join messages to invocations must filter `messages.dispatch_state >= 1000`.
- If skipped-message audit detail becomes necessary, add a separate reason field instead of overloading more sentinel values.
