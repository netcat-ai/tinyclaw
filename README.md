# tinyclaw

TinyClaw is a room-scoped agent runtime control plane. The current implementation is centered on the Core Model:

- **Room**: TinyClaw-owned conversation container mapped from an external Channel Room.
- **Agent Session**: one configured agent context inside a Room, with its own trigger and processed-message checkpoint.
- **Message**: inbound fact for exactly one Room.
- **Room Memory**: durable Room-owned knowledge searched by Agent Sessions during Agent Runs.
- **Delivery**: outbound item produced by an agent run.
- **Command**: explicit Room message handled by Clawman without ordinary agent conversation.

## Current State

The current codebase has removed the old in-repo agent runtime, sandbox orchestrator, and `RoomChat` gRPC bridge. Channel-specific ingestion and sending should live in external Channel Adapters that call TinyClaw APIs.

Triggered Agent Sessions are picked up by an in-process execution loop. Until an agent runner is configured, triggered runs fail fast and produce a failure delivery.

Set `AGENT_RUNNER=codex` to run triggered Agent Sessions through `codex exec`. Optional Codex runner settings:

- `CODEX_BIN`: Codex executable, defaults to `codex`.
- `CODEX_WORKDIR`: working directory passed to Codex, defaults to `.`.
- `CODEX_MODEL`: optional model override.
- `CODEX_SANDBOX`: Codex sandbox mode, defaults to `workspace-write`.
- `CODEX_DISABLED_FEATURES`: comma-separated Codex CLI features disabled for headless runs, defaults to `apps,tool_suggest,plugins`; set to `none` to disable no features.
- `CODEX_RUNNER_TIMEOUT`: execution timeout, defaults to `5m`.

Codex runs receive a short-lived Room Memory Search capability. The capability calls Clawman's internal memory endpoint with a run-bound token; it does not accept `room_id` from the agent. Runner output is parsed as an Agent Run Result with user-visible final output and optional Memory Write Proposals. Memory Search failures are returned to Codex as error results so the run can continue; memory writes are persisted as background jobs and do not block Delivery creation.

The Codex runner reuses one Codex CLI thread per Agent Session. Clawman stores the Codex `thread_id` on `agent_sessions.codex_session_id`; subsequent runs call `codex exec resume <codex_session_id> -`. If the saved thread is stale, the runner falls back to a fresh Codex thread and stores the new id.

First-version `/draw <prompt>` is designed as a Clawman-owned Command rather than ordinary Codex conversation. It saves the Message, does not trigger the ordinary Agent Session, starts an in-process background draw task for new non-duplicate Messages, calls `gpt-image-2`, uploads the PNG to S3-compatible object storage, and emits command Deliveries. The image Delivery carries a 24h presigned S3 URL instead of embedding image bytes.

Kubernetes deployment pins `api.openai.com` and `chatgpt.com` through `hostAliases` because the current cluster DNS resolves those domains incorrectly. Refresh those IPs if Codex CLI connectivity starts timing out before `thread.started`.

`clawman` now exposes the Core Model HTTP interface:

- `POST /api/rooms`
- `POST /api/messages`
- `GET /api/deliveries?channel=<channel>&id=<last_id>`
- `POST /api/deliveries/{id}/ack`

`POST /api/inbound` has been removed; adapters must register a Room first, then submit Messages with `room_id`.

API requests use `Authorization: Bearer $CLAWMAN_API_TOKEN`.

## Package Layout

```text
internal/core      Core Model types and Trigger Policy
internal/storage   PostgreSQL implementation for Core Model persistence
internal/api       HTTP adapter for Core Model routes
internal/executor  Agent execution loop, memory write worker, and runner context
channel/wecom/     Legacy WeCom SDK helpers and clients
```

The root `main` package wires configuration, storage, metrics, and the Core Model API.

## Integration Status

2026-05-20 local phone integration passed for the Enterprise WeChat path:

```text
POST /api/rooms -> POST /api/messages -> Codex runner -> delivery -> MobileClaw poll -> Enterprise WeChat send -> ack
```

The verified MobileClaw delivery payload uses:

- `app` / `channel`: `wecom`
- `recipient_alias` / `channel_room_id`: Enterprise WeChat display alias
- `text`: final Codex output

The WeChat path is not verified on the current test phone because the installed WeChat exposes an empty accessibility node tree to MobileClaw.

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

Draw command and generated media:

- `DRAW_COMMAND_ENABLED`: enable `/draw`, defaults to true.
- `IMAGE_PROVIDER_BASE_URL`: image provider endpoint, defaults to `https://code.v4.chat`.
- `IMAGE_PROVIDER_MODEL`: image model, defaults to `gpt-image-2`.
- `IMAGE_PROVIDER_API_KEY`: optional image provider key; when empty, first-version `/draw` may read `CODEX_AUTH_JSON.OPENAI_API_KEY`.
- `DRAW_IMAGE_SIZE`: image size sent to the provider, defaults to `1024x1024`.
- `GENERATED_MEDIA_S3_ENDPOINT`
- `GENERATED_MEDIA_S3_BUCKET`
- `GENERATED_MEDIA_S3_REGION`
- `GENERATED_MEDIA_S3_ACCESS_KEY_ID`
- `GENERATED_MEDIA_S3_SECRET_ACCESS_KEY`
- `GENERATED_MEDIA_S3_FORCE_PATH_STYLE`: set for S3-compatible providers that require path-style addressing.
- `GENERATED_MEDIA_URL_TTL`: presigned URL lifetime, defaults to `24h`.

Channel adapters own provider-specific configuration. The current deployable binary can run the WeCom archive adapter in-process as a deployment bridge by setting `WECOM_ENABLED=true`. This keeps the Core Model API stable while avoiding a second service until the adapter is split out.

WeCom archive adapter:

- `WECOM_ENABLED`: enable in-process WeCom archive polling, defaults to false.
- `WECOM_CORP_ID`
- `WECOM_CORP_SECRET`
- `WECOM_CONTACT_SECRET`: secret used for WeCom contact/group metadata lookups; falls back to `WECOM_CORP_SECRET`.
- `WECOM_RSA_PRIVATE_KEY`
- `WECOM_BOT_ID`: used to skip self-sent messages and identify direct chat peers.
- `WECOM_POLL_INTERVAL`: defaults to `3s`.
- `WECOM_POLL_LIMIT`: defaults to `100`.
- `WECOM_SDK_TIMEOUT`: defaults to `30`.
- `WECOM_START_SEQ`: initial archive seq when no adapter cursor exists, defaults to `0`.

The adapter stores archive progress in `channel_adapter_cursors`. WeCom archive `seq` remains an adapter-local cursor and is not used as the TinyClaw Message identity.

Mobile delivery uses `rooms.outbound_alias` as `recipient_alias`, falling back to `display_name` and then `channel_room_id`. For WeCom, the in-process adapter resolves a room/contact name before registering the Room.

## Design Docs

- [QA](./docs/QA.md)
- [Core Model Refactor V1](./docs/CORE_MODEL_REFACTOR_V1.md)
- [Next Steps](./docs/NEXT_STEPS.md)
- [Append-Only Room Messages ADR](./docs/adr/0001-append-only-room-messages.md)
- [Room-Owned Memory ADR](./docs/adr/0002-use-room-owned-memory.md)
- Historical docs such as [Architecture V0](./docs/ARCHITECTURE_V0.md), [Architecture Refactor 2026-04](./docs/ARCHITECTURE_REFACTOR_2026_04.md), [Agent Sandbox Integration V0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md), and [Room Memory V0](./docs/ROOM_MEMORY_V0.md) no longer describe current code.
