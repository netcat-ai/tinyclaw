# PROTOTYPE: Core model state

Throwaway prototype for the TinyClaw core model state machine.

Question:

Does the proposed `Room -> Message -> Invocation -> Delivery` model feel right when driven through common cases: non-trigger context accumulation, trigger binding, active invocation append, duplicate inbound idempotency, failure delivery, empty output, delivery creation, and delivery ack?

Run:

```sh
go run ./prototype/core_model_state
```

This prototype is in-memory only. Delete it or fold the validated reducer into real code after the model is settled.
