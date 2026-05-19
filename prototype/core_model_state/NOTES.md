# Prototype Notes

Question:

Does the proposed `Room -> Message -> Invocation -> Delivery` model feel right when driven by hand?

Verdict:

Manual exploration found the current model is mostly coherent, but several decisions need tightening before implementation:

1. Failed invocations are terminal in the current model. A failure creates a failure Delivery, and later user input waits for a new trigger instead of retrying the failed Invocation in place.
2. Binding every `dispatch_state = 0` message on trigger works for simple group context, but it needs a context window or max count before production; otherwise old untriggered messages can grow without bound.
3. `dispatch_state = 1` is operationally enough for skipping, but debugging will be weak without at least an optional `skip_reason`.
4. Delivery ack being separate from invocation completion feels right. It avoids rerunning agent logic when outbound delivery is delayed or retried.
5. Empty output with zero delivery feels valid, but the UI/API will need a way to distinguish "completed intentionally with no reply" from "completed but no output due to bug" if this matters operationally.
