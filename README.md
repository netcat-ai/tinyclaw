# tinyclaw

TinyClaw is a room-scoped agent runtime control plane. The current implementation is centered on the Core Model:

- **Room**: TinyClaw-owned conversation container mapped from an external Channel Room.
- **Message**: inbound fact for exactly one Room.
- **Invocation**: one agent execution attempt for a Room.
- **Delivery**: outbound item produced by an Invocation.

## Current State

The current codebase has removed the old in-repo agent runtime, sandbox orchestrator, and `RoomChat` gRPC bridge. Channel-specific ingestion and sending should live in external Channel Adapters that call TinyClaw APIs.

`clawman` now exposes the Core Model HTTP interface:

- `POST /api/inbound`
- `GET /api/deliveries?channel=<channel>&seq=<last_seq>`
- `POST /api/deliveries/{id}/ack`
- `POST /api/invocations/{id}/complete`
- `POST /api/invocations/{id}/fail`

API requests use `Authorization: Bearer $CLAWMAN_API_TOKEN`.

## Package Layout

```text
internal/core      Core Model types and Trigger Policy
internal/storage   PostgreSQL implementation for Core Model persistence
internal/api       HTTP adapter for Core Model routes
wecom/             Legacy WeCom SDK helpers and clients
```

The root `main` package wires configuration, storage, metrics, the Core Model API, and remaining legacy WeCom archive ingestion.

## Build And Test

```bash
go test ./...
go build -o tinyclaw .
```

The WeCom Finance SDK native implementation only builds on Linux. Other platforms use the unsupported stub.

## Configuration

Main service:

- `DATABASE_URL`
- `CLAWMAN_API_TOKEN`
- `CONTROL_API_ADDR` default `:8081`
- `CLAWMAN_INTERNAL_TOKEN`
- `METRICS_ADDR` default `:9090`

Legacy WeCom archive ingestion:

- `WECOM_CORP_ID`
- `WECOM_CORP_SECRET`
- `WECOM_RSA_PRIVATE_KEY`
- `WECOM_CONTACT_SECRET`
- `WECOM_BOT_ID`
- `WECOM_GROUP_TRIGGER_MENTIONS`
- `WECOM_GROUP_TRIGGER_KEYWORDS`

## Design Docs

- [Core Model Refactor V1](./docs/CORE_MODEL_REFACTOR_V1.md)
- [Message Invocation State ADR](./docs/adr/0001-message-invocation-state-sentinel.md)
- [Agent Sandbox Integration V0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md) is historical and no longer describes current code.
