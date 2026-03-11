# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is tinyclaw

Cloud-based AI Agent Runtime for WeChat Work (企业微信). The main service (`clawman`) pulls encrypted messages from WeChat Work's Finance SDK, decrypts them, and dispatches to per-room Redis Streams. Agents in isolated K8s sandboxes consume from their room stream via `XREADGROUP BLOCK`.

## Build & Run

```bash
go build -o tinyclaw .              # CGO required (Finance SDK is native C)
go test ./...                       # run all tests
docker build -t tinyclaw:latest .   # multi-stage: Go 1.24 builder + Debian bookworm-slim runtime
```

The Finance SDK native library only compiles on Linux (`wecom/finance/sdk_linux.go`). Other platforms get a stub (`sdk_unsupport.go`).

Required env vars: `WECOM_CORP_ID`, `WECOM_CORP_SECRET`, `WECOM_RSA_PRIVATE_KEY`. See `config.go` for all options with defaults.

## Architecture

```
WeChat Work Finance SDK → Clawman (3s poll loop) → Redis Stream per room → Agent sandbox (XREADGROUP BLOCK)
```

- `main.go` — entry point: Redis client, Resolver, Clawman init, signal handling
- `clawman.go` — ingress service: pulls messages via Finance SDK, decrypts, parses, dispatches to Redis Streams
- `clawman.go` — resolves direct-message identities and group metadata via WeCom APIs with 1h Redis cache
- `config.go` — all config from env vars with `envOrDefault` pattern
- `wecom/client.go` — minimal WeChat Work API client with mutex-guarded token refresh
- `wecom/contact.go` — external contact and group chat resolution APIs
- `wecom/finance/` — CGO wrapper around native WeChat Work Finance SDK + RSA decryption

### Redis key conventions

| Pattern | Purpose |
|---------|---------|
| `stream:room:{roomID}` | Per-room message stream |
| `msg:seq` | Last processed WeChat Work sequence number |
| `wecom:contact:external:{id}` | External contact cache (1h TTL) |
| `wecom:user:internal:{id}` | Internal user cache (1h TTL) |
| `wecom:group:detail:{roomID}` | Group detail cache (1h TTL) |
| `wecom:group:owner:{roomID}` | Group owner cache (1h TTL) |
| `lock:ensure:{room_id}` | Ensure-once lock (3s TTL) |

### Message flow

1. Finance SDK returns encrypted `ChatData` batches starting from stored `seq`
2. Each message is RSA-decrypted, JSON-parsed, validated (must have `from` + `tolist`)
3. Valid messages are `XADD`'d to the room stream with fields `msgid` and `raw`
4. Sequence is persisted to Redis after each successful dispatch
5. Invalid/undecryptable messages are skipped with a log line

### WeChat Work detail routing

- Direct message sender IDs prefixed with `wm` or `wo` → external contact API lookup.
- Other direct message sender IDs → internal user API lookup.
- Group `roomid` values try archive-group lookup first, then fall back to customer-group lookup.

## Deployment

K8s namespace: `claw`. Deployment name: `clawman`. Image pushed to `ghcr.io/<owner>/tinyclaw`.

CI is two workflows:
- `build.yml` — test + Docker build on every push/PR; image push only on `main`
- `deploy.yml` — triggered after successful build on `main`; connects via Tailscale OAuth then `kubectl apply`

## Conventions

- Commit format: `<type>: <summary>` (e.g., `docs: clarify room_id rules`)
- One logical change per commit
- Filenames: uppercase snake-style with version suffix for docs (e.g., `ARCHITECTURE_V0.md`)
- Reuse existing terminology from `docs/ARCHITECTURE_V0.md` — don't invent new terms for established concepts
- Error handling: skip-and-log for individual message failures, return error for infrastructure failures (Redis, SDK init)
- Keep docs atomic — update existing files rather than creating overlapping documents
