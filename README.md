# tinyclaw

TinyClaw is a room-scoped agent runtime control plane. The current implementation is centered on the Core Model:

- **Room**: TinyClaw-owned conversation container mapped from an external Channel Room.
- **Message**: inbound fact for exactly one Room.
- **Invocation**: one agent execution attempt for a Room.
- **Delivery**: outbound item produced by an Invocation.

## Current State

The current codebase has removed the old in-repo agent runtime, sandbox orchestrator, and `RoomChat` gRPC bridge. Channel-specific ingestion and sending should live in external Channel Adapters that call TinyClaw APIs.

Triggered invocations are started by an in-process execution module. Until a real agent runner is configured, triggered invocations fail fast and produce a failure delivery.

`clawman` now exposes the Core Model HTTP interface:

- `POST /api/inbound`
- `GET /api/deliveries?channel=<channel>&id=<last_id>`
- `POST /api/deliveries/{id}/ack`
- `POST /api/invocations/{id}/complete`
- `POST /api/invocations/{id}/fail`

API requests use `Authorization: Bearer $CLAWMAN_API_TOKEN`.

## Package Layout

```text
internal/core      Core Model types and Trigger Policy
internal/storage   PostgreSQL implementation for Core Model persistence
internal/api       HTTP adapter for Core Model routes
internal/executor  Invocation execution module and runner context
channel/wecom/     Legacy WeCom SDK helpers and clients
```

The root `main` package wires configuration, storage, metrics, and the Core Model API.

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
- `METRICS_ADDR` default `:9090`

Channel adapters own provider-specific configuration. The core service does not read `WECOM_*` environment variables.

## Design Docs

- [Core Model Refactor V1](./docs/CORE_MODEL_REFACTOR_V1.md)
- [Append-Only Room Messages ADR](./docs/adr/0001-append-only-room-messages.md)
- [Agent Sandbox Integration V0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md) is historical and no longer describes current code.
