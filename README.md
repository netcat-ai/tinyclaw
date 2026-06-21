# tinyclaw

TinyClaw is a room-scoped agent runtime control plane. The current implementation is centered on the Core Model:

- **Room**: shared collaboration space over one append-only Message timeline.
- **Agent Session**: one Room-scoped default orchestrator runtime state, with trigger and caught-up boundaries.
- **Agent**: user-owned configurable agent definition. Private Agents are editable user assets; shared Agents may be addressed in Rooms and injected as run-scoped Subagents.
- **Message**: append-only channel-shaped fact resolved into exactly one Room.
- **Room Memory**: durable Room-owned knowledge searched during Agent Runs.
- **Delivery**: channel-bound outbound intent produced by an agent run or command.
- **Command**: explicit Room message handled by Clawman without ordinary agent conversation.

## Current State

The current codebase has removed the old in-repo agent runtime, sandbox orchestrator, and `RoomChat` gRPC bridge. Channel-specific ingestion and sending should live in external Channel Adapters that call TinyClaw APIs.

Triggered Agent Sessions are picked up by an in-process execution loop. First version has exactly one Agent Session per Room. Until an agent runner is configured, triggered runs fail fast and produce a failure delivery.

Set `AGENT_RUNNER=codex` to run triggered Agent Sessions through `codex exec`. Optional Codex runner settings:

- `CODEX_BIN`: Codex executable, defaults to `codex`.
- `CODEX_WORKDIR`: working directory passed to Codex, defaults to `.`.
- `CODEX_MODEL`: optional model override.
- `CODEX_SANDBOX`: Codex sandbox mode, defaults to `workspace-write`; use `danger-full-access` when headless runs need external network/DNS from shell tools.
- `CODEX_OPENAI_BASE_URL`: optional OpenAI-compatible API base URL passed to `codex exec` as `openai_base_url`.
- `CODEX_DISABLED_FEATURES`: comma-separated Codex CLI features disabled for headless runs, defaults to `apps,tool_suggest,plugins`; set to `none` to disable no features.
- `CODEX_RUNNER_TIMEOUT`: execution timeout, defaults to `5m`.
- `AGENT_WORKER_CONCURRENCY`: number of concurrent Agent Run workers, defaults to `2`. Runs stay serialized per Room through Agent Session locks while different Rooms can run concurrently.

Codex runs receive a short-lived Room Memory Search capability. The capability calls Clawman's internal memory endpoint with a run-bound token; it does not accept `room_id` from the agent. Runner output is parsed as an Agent Run Result with user-visible final output, optional Memory Write Proposals, and optional image generation requests. Memory Search failures are returned to Codex as error results so the run can continue; memory writes are persisted as background jobs and do not block Delivery creation.

Media-bearing context messages are not preattached to Codex. The Codex runner passes `TINYCLAW_MEDIA_BASE_URL` and `TINYCLAW_MEDIA_DOWNLOAD_DIR` through the process environment. Run-scoped semantic input, such as Room Prompt, selected agents, and Memory Search results, is represented as typed JSONL context messages. Codex should download only needed media with `curl -L "$TINYCLAW_MEDIA_BASE_URL/internal/media?msgid=$message_id" -o "$TINYCLAW_MEDIA_DOWNLOAD_DIR/$message_id"` and then inspect the local file.

The Codex runner reuses one Codex CLI thread per Agent Session. Clawman stores the Codex `thread_id` on `agent_sessions.codex_session_id`; subsequent runs call `codex exec resume <codex_session_id> -`. If the saved thread is stale, the runner falls back to a fresh Codex thread and stores the new id.

First-version `/draw <prompt>` is a Clawman-owned Command shortcut rather than ordinary Codex conversation. It saves the Message, does not advance the Agent Session trigger boundary, starts an in-process background draw task for new non-duplicate Messages, calls `gpt-image-2`, uploads the PNG to S3-compatible object storage, and emits command Deliveries. The image Delivery carries a 24h presigned S3 URL instead of embedding image bytes. `/draw 图生图 <prompt>` and `/draw 把上图...` use the latest previous image Message in the same Room as the source image and call the image edit endpoint after downloading that source through Clawman's internal media URL. Ordinary Codex Agent Runs can also request generated media through structured `image_generation_requests`; Clawman performs provider calls, storage, and image Deliveries.

Kubernetes deployment pins `api.openai.com` and `chatgpt.com` through `hostAliases` because the current cluster DNS resolves those domains incorrectly. Refresh those IPs if Codex CLI connectivity starts timing out before `thread.started`.

`clawman` now exposes the Core Model HTTP interface:

- `POST /api/rooms`
- `POST /api/messages`
- `GET /api/deliveries?channels=<channel1>,<channel2>`
- `POST /api/deliveries/{id}/ack`

`POST /api/inbound` has been removed; adapters must register a Room first, then submit Messages with `room_id`.

TinyClaw stores provider-shaped Message headers as first-class columns: `source`, `msgid`, `action`, `from_id`, `tolist`, `roomid`, `msgtime`, and `msgtype`. Type-specific content lives in `messages.body`. Channel Adapters must use `(room_id, source, msgid)` as the idempotency key and keep adapter-local cursors outside TinyClaw.

API requests use `Authorization: Bearer $CLAWMAN_API_TOKEN`.
Admin API requests use HTTP Basic auth with `client_id=admin` and `client_secret=$CLAWMAN_ADMIN_SECRET`.

## Package Layout

```text
internal/core      Core Model types and Trigger Policy
internal/storage   PostgreSQL implementation for Core Model persistence
internal/api       HTTP adapter for Core Model routes
internal/executor  Agent execution loop, memory write worker, and runner context
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
go build -o clawman .
```

CI builds the Clawman service image from a static Linux binary and the Control Plane build output. WeCom and WeChat adapters are not packaged into the Clawman image; deploy them from `tinybridge` as separate adapter processes when validating real channels.

## Local Run

Enterprise WeChat archive ingestion depends on the WeCom finance SDK and is best validated on Linux. For macOS local validation, run Clawman with PostgreSQL and use `local` or `wechat` channel messages.

```bash
./scripts/local_start.sh
```

Open the Control Plane at `http://127.0.0.1:8081/admin/` and log in with `admin` / `dev-admin`.
The local process writes its PID and log to `.local/clawman.pid` and `.local/clawman.log`. Agent Run logs include run boundaries, selected run-scoped Subagents, Memory Search count, Memory Write count, and Delivery id. For foreground debugging, run:

```bash
TINYCLAW_FOREGROUND=true ./scripts/local_start.sh
```

Container local run:

```bash
./scripts/local_stop.sh
docker compose -f compose.local.yml up -d postgres clawman tinybridge
```

`compose.local.yml` runs Postgres, Clawman, and `tinybridge/cmd/woc-adapter`. Clawman exposes `8081` and `9090`; `tinybridge` talks to Clawman at `http://clawman:8081` and to a host-local WechatOnCloud Panel through `http://host.docker.internal:36080` by default. Codex sends requests through `CODEX_OPENAI_BASE_URL` which defaults to `https://code.v4.chat`.

For container local run with `AGENT_RUNNER=codex`, put Codex CLI state under `.local/codex/`. At minimum it must contain `auth.json` and `config.toml`; the directory is mounted to `/codex` and is intentionally ignored by Git.

Minimal local smoke:

```bash
./scripts/local_smoke.sh
./scripts/local_admin_smoke.sh
./scripts/local_wechat_adapter_smoke.sh
```

The core smoke script registers a temporary local Room, submits a Message, waits for an `agent_output` Delivery, prints it, and acks it. The admin smoke script checks Control Plane API credentials, Agent create/update, Admin Room registration, Inject Message, Timeline, and Memory endpoints. The WeChat adapter smoke script runs `tinybridge/cmd/wechat-adapter` against a temporary fake `wx` CLI and verifies it registers a `wechat` Room plus a provider-shaped Message without requiring a real WeChat client. Set `AGENT_RUNNER=codex` when you want triggered Agent Sessions to call `codex exec`; otherwise triggered runs intentionally produce an `agent_failure` Delivery.

Check local dependencies, Postgres health, Clawman health, the Admin UI, and metrics with:

```bash
./scripts/local_status.sh
```

Run local verification with:

```bash
./scripts/local_verify.sh
```

Set `TINYCLAW_VERIFY_FULL=true` to include PostgreSQL-backed E2E tests. Full verification resets the local test database before the final smoke repopulates a demo Room.

Stop the local Clawman process with:

```bash
./scripts/local_stop.sh
```

## Configuration

Main service:

- `DATABASE_URL`
- `CLAWMAN_API_TOKEN`
- `CLAWMAN_ADMIN_SECRET`: enables the built-in `admin` client for `/admin/api/*` without writing it to the database.
- `CONTROL_API_ADDR` default `:8081`
- `METRICS_ADDR` default `:9090`

Draw command and generated media:

- `DRAW_COMMAND_ENABLED`: enable `/draw`, defaults to true.
- `IMAGE_PROVIDER_BASE_URL`: image provider endpoint, defaults to `https://code.v4.chat`.
- `IMAGE_PROVIDER_MODEL`: image model, defaults to `gpt-image-2`.
- `IMAGE_PROVIDER_API_KEY`: optional image provider key; when empty, first-version `/draw` may read `OPENAI_API_KEY` from Codex auth JSON via `CODEX_AUTH_JSON` or `CODEX_AUTH_PATH`.
- `DRAW_IMAGE_SIZE`: image size sent to the provider, defaults to `1024x1024`.
- `GENERATED_MEDIA_S3_ENDPOINT`
- `GENERATED_MEDIA_S3_BUCKET`
- `GENERATED_MEDIA_S3_REGION`
- `GENERATED_MEDIA_S3_ACCESS_KEY_ID`
- `GENERATED_MEDIA_S3_SECRET_ACCESS_KEY`
- `GENERATED_MEDIA_S3_FORCE_PATH_STYLE`: set for S3-compatible providers that require path-style addressing.
- `GENERATED_MEDIA_URL_TTL`: presigned URL lifetime, defaults to `24h`.

Channel adapters own provider-specific configuration and live outside the Clawman service. The checked-in `tinybridge` submodule contains the current WeChat and WeCom adapters:

- `tinybridge/cmd/wechat-adapter`
- `tinybridge/cmd/wecom-archive-adapter`

Run adapter commands from the `tinybridge` module. TinyClaw only exposes the adapter-facing Core Model APIs and Delivery polling endpoints.

## Design Docs

Current docs:

- [Core Model Refactor V1](./docs/CORE_MODEL_REFACTOR_V1.md): source of truth for Room, Message, Agent Session, Agent, Delivery, and Memory.
- [Next Steps](./docs/NEXT_STEPS.md): current implementation order.
- [QA](./docs/QA.md): current production/debug checklist.
- [Append-Only Room Messages ADR](./docs/adr/0001-append-only-room-messages.md)
- [Room-Owned Memory ADR](./docs/adr/0002-use-room-owned-memory.md)

Historical docs are retained only for decision history and must not be used as current contracts: [Architecture V0](./docs/ARCHITECTURE_V0.md), [Architecture Refactor 2026-04](./docs/ARCHITECTURE_REFACTOR_2026_04.md), [Agent Sandbox Integration V0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md), [Room Memory V0](./docs/ROOM_MEMORY_V0.md), and [Conversation Log](./docs/CONVERSATION_LOG.md).
