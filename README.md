# tinyclaw

TinyClaw is a room-scoped agent runtime control plane. The current implementation is centered on the Core Model:

- **Room**: shared collaboration space over one append-only Message timeline.
- **Agent Session**: one Room-scoped default orchestrator runtime state, with trigger and caught-up boundaries.
- **Agent**: configurable agent definition that may be addressed in a Room or executed as a run-scoped Subagent.
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
- `CODEX_SANDBOX`: Codex sandbox mode, defaults to `workspace-write`.
- `CODEX_DISABLED_FEATURES`: comma-separated Codex CLI features disabled for headless runs, defaults to `apps,tool_suggest,plugins`; set to `none` to disable no features.
- `CODEX_RUNNER_TIMEOUT`: execution timeout, defaults to `5m`.

Codex runs receive a short-lived Room Memory Search capability. The capability calls Clawman's internal memory endpoint with a run-bound token; it does not accept `room_id` from the agent. Runner output is parsed as an Agent Run Result with user-visible final output and optional Memory Write Proposals. Memory Search failures are returned to Codex as error results so the run can continue; memory writes are persisted as background jobs and do not block Delivery creation.

The Codex runner reuses one Codex CLI thread per Agent Session. Clawman stores the Codex `thread_id` on `agent_sessions.codex_session_id`; subsequent runs call `codex exec resume <codex_session_id> -`. If the saved thread is stale, the runner falls back to a fresh Codex thread and stores the new id.

First-version `/draw <prompt>` is designed as a Clawman-owned Command rather than ordinary Codex conversation. It saves the Message, does not advance the Agent Session trigger boundary, starts an in-process background draw task for new non-duplicate Messages, calls `gpt-image-2`, uploads the PNG to S3-compatible object storage, and emits command Deliveries. The image Delivery carries a 24h presigned S3 URL instead of embedding image bytes.

Kubernetes deployment pins `api.openai.com` and `chatgpt.com` through `hostAliases` because the current cluster DNS resolves those domains incorrectly. Refresh those IPs if Codex CLI connectivity starts timing out before `thread.started`.

`clawman` now exposes the Core Model HTTP interface:

- `POST /api/rooms`
- `POST /api/messages`
- `GET /api/deliveries?channels=<channel1>,<channel2>`
- `POST /api/deliveries/{id}/ack`

`POST /api/inbound` has been removed; adapters must register a Room first, then submit Messages with `room_id`.

TinyClaw currently stores inbound content as `messages.payload`; the next message-schema refactor should keep provider-shaped headers (`msgid`, `from`, `tolist`, `roomid`, `msgtime`, `msgtype`) separate from the type-specific Message Body. Until that schema lands, Channel Adapters should preserve provider raw fields inside `payload` and use `source_message_id` for idempotency.

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
go build -o tinyclaw .
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
- `IMAGE_PROVIDER_API_KEY`: optional image provider key; when empty, first-version `/draw` may read `CODEX_AUTH_JSON.OPENAI_API_KEY`.
- `DRAW_IMAGE_SIZE`: image size sent to the provider, defaults to `1024x1024`.
- `GENERATED_MEDIA_S3_ENDPOINT`
- `GENERATED_MEDIA_S3_BUCKET`
- `GENERATED_MEDIA_S3_REGION`
- `GENERATED_MEDIA_S3_ACCESS_KEY_ID`
- `GENERATED_MEDIA_S3_SECRET_ACCESS_KEY`
- `GENERATED_MEDIA_S3_FORCE_PATH_STYLE`: set for S3-compatible providers that require path-style addressing.
- `GENERATED_MEDIA_URL_TTL`: presigned URL lifetime, defaults to `24h`.

Channel adapters own provider-specific configuration and live in the `tinybridge` submodule.

Local WeChat adapter:

- Run with `cd tinybridge && go run ./cmd/wechat-adapter`.
- It is a local-only process because it depends on the local `wx` CLI and local WeChat data.
- By default it tries `wx history <chat> --json`, then falls back to filtered `wx new-messages --json`.
- Set `WECHAT_READ_MODE=new-messages` to let wx-cli provide the local "new message" feed.
- Do not deploy it inside the Clawman service or container.
- It does not send WeChat messages directly; MobileClaw should poll `GET /api/deliveries?channels=wecom,wechat` and send/ack on the phone.
- Incoming WeChat image messages are currently ingested as image metadata (`type=image`, `text=[图片]`, `wechat_local_id`) because `wx` exposes `local_id` but not the image file path through `history` or `new-messages`.
- For WOC-based WeChat ingestion, prefer an instance-side push hook that sends new-message batches to the adapter; TinyClaw should not own WOC cursors or decrypted database state.
- `CLAWMAN_API_TOKEN`: required, same bearer token used by Clawman.
- `CLAWMAN_BASE_URL`: defaults to `http://127.0.0.1:8081`.
- `WECHAT_WX_BIN`: defaults to `wx`.
- `WECHAT_TARGET_CHATS`: comma-separated allowed WeChat room ids or names. Defaults to empty. Prefer stable ids such as `wxid_...` or `...@chatroom`; display names can collide between WeChat and OpenIM/WeCom contacts.
- `WECHAT_TARGET_MEMBERS`: comma-separated WeChat member ids such as `wxid_...`. A direct chat with that id is ingested; a group chat is ingested when `wx members <group> --json` contains that id. Group membership checks are cached in memory for the adapter process.
- `WECHAT_GROUP_ID`: fallback chat room id for `history` mode and legacy tests, defaults to `50261801724@chatroom`.
- `WECHAT_GROUP_NAME`: fallback chat display name for `history` mode and legacy tests, defaults to `测试群`.
- `WECHAT_HISTORY_CHAT`: optional chat lookup override for `wx history`.
- `WECHAT_READ_MODE`: defaults to `auto`; optional `history` or `new-messages`.
- `WECHAT_TRIGGER_POLICY`: defaults to `{"mode":"always"}` for test-group validation.
- `WECHAT_POLL_INTERVAL`: defaults to `3s`.
- `WECHAT_POLL_LIMIT`: defaults to `200`.
- `WECHAT_ONCE`: set to `true` to register the Room, run one poll, submit matching Messages, and exit.
- `WECHAT_SELF_SENDERS`: comma-separated sender display names used by the adapter to avoid triggering on self-sent messages, defaults to `私云虾虾`.

Test-group smoke flow:

```bash
export CLAWMAN_API_TOKEN=...
export CLAWMAN_BASE_URL=http://127.0.0.1:8081
export WECHAT_GROUP_ID=50261801724@chatroom
export WECHAT_GROUP_NAME=测试群
export WECHAT_TARGET_CHATS=50261801724@chatroom
export WECHAT_TARGET_MEMBERS=
cd tinybridge
WECHAT_ONCE=true go run ./cmd/wechat-adapter
curl -H "Authorization: Bearer $CLAWMAN_API_TOKEN" \
  "$CLAWMAN_BASE_URL/api/deliveries?channels=wechat"
```

WeCom archive adapter:

- Run with `cd tinybridge && go run ./cmd/wecom-archive-adapter`.
- `WECOM_CORP_ID`
- `WECOM_CORP_SECRET`
- `WECOM_CONTACT_SECRET`: secret used for WeCom contact/group metadata lookups; falls back to `WECOM_CORP_SECRET`.
- `WECOM_RSA_PRIVATE_KEY`
- `WECOM_BOT_ID`: used to skip self-sent messages and identify direct chat peers.
- `WECOM_POLL_INTERVAL`: defaults to `3s`.
- `WECOM_POLL_LIMIT`: defaults to `100`.
- `WECOM_SDK_TIMEOUT`: defaults to `30`.
- `WECOM_START_SEQ`: initial archive seq when no adapter cursor exists, defaults to `0`.
- `TINYBRIDGE_CURSOR_PATH`: adapter cursor state file, defaults to `.tinybridge/cursors.json`.

The adapter stores archive progress in its own state file. WeCom archive `seq` remains an adapter-local cursor and is not used as the TinyClaw Message identity.

Mobile delivery uses `rooms.outbound_alias` as `recipient_alias`, falling back to `display_name` and then `channel_room_id`. For WeCom, TinyBridge resolves a room/contact name before registering the Room.

## Design Docs

Current docs:

- [Core Model Refactor V1](./docs/CORE_MODEL_REFACTOR_V1.md): source of truth for Room, Message, Agent Session, Agent, Delivery, and Memory.
- [Next Steps](./docs/NEXT_STEPS.md): current implementation order.
- [QA](./docs/QA.md): current production/debug checklist.
- [Append-Only Room Messages ADR](./docs/adr/0001-append-only-room-messages.md)
- [Room-Owned Memory ADR](./docs/adr/0002-use-room-owned-memory.md)

Historical docs are retained only for decision history and must not be used as current contracts: [Architecture V0](./docs/ARCHITECTURE_V0.md), [Architecture Refactor 2026-04](./docs/ARCHITECTURE_REFACTOR_2026_04.md), [Agent Sandbox Integration V0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md), [Room Memory V0](./docs/ROOM_MEMORY_V0.md), and [Conversation Log](./docs/CONVERSATION_LOG.md).
