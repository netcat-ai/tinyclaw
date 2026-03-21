# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## What is tinyclaw

Cloud-based AI Agent Runtime for WeChat Work (企业微信).

Current architecture:
- `clawman` pulls encrypted messages from the WeChat Work Finance SDK.
- `clawman` splits ingest and dispatch into separate loops; `messages.seq` is the archive checkpoint.
- `clawman` uses the official `agent-sandbox` Go SDK to open and reuse sandbox clients.
- `clawman` invokes the sandbox through `sandbox-router` over HTTP.
- The sandboxed `agent` exposes `/healthz`, `/agent`, `/execute`, and the standard file APIs.
- All pulled archive items are persisted to PostgreSQL `messages`, and replies are sent by the egress consumer from `outbox_deliveries`.

## Build & Run

```bash
go build -o tinyclaw .              # CGO required (Finance SDK is native C)
go test ./...                       # run all Go tests
cd agent && npm test                # run agent tests
docker build -t tinyclaw:latest .   # main service image
```

The Finance SDK native library only compiles on Linux (`wecom/finance/sdk_linux.go`). Other platforms get a stub (`sdk_unsupport.go`).

Required env vars for the main service:
- `WECOM_CORP_ID`
- `WECOM_CORP_SECRET`
- `WECOM_RSA_PRIVATE_KEY`

See `config.go` for the current main-service env contract.

## Architecture

```text
WeChat Work Finance SDK
  -> Clawman ingest (poll / decrypt / normalize / persist)
  -> PostgreSQL messages(seq/status/payload)
  -> Clawman dispatch (scan pending rooms)
  -> agent-sandbox Go SDK open
  -> sandbox-router
  -> agent HTTP runtime (/agent)
  -> WorkTool / WeCom send
```

Key files:
- `main.go` — main service entry point
- `clawman.go` — ingress pull loop, WeCom metadata resolution, sandbox invocation
- `sandbox/ensure.go` — official Go SDK room client manager + `/agent` bridge
- `sandbox/router.go` — router HTTP client
- `agent/src/main.ts` — sandbox HTTP runtime entry
- `agent/src/server.ts` — `/healthz`, `/agent`, `/execute`, `/upload`, `/download`, `/list`, `/exists`
- `agent/src/runtime.ts` — echo / `claude_agent_sdk`

## Minimal PostgreSQL tables

| Table | Purpose |
|---------|---------|
| `messages` | Inbound archive facts, status machine, and `seq` checkpoint source |
| `outbox_deliveries` | Pending / retry / sent / failed egress jobs |

Short-lived WeCom detail caching and ensure debounce are process-local in the current single-replica version.

## Conventions

- Commit format: `<type>: <summary>` (for example `docs: clarify sandbox claim flow`)
- One logical change per commit
- Prefer updating existing docs over creating overlapping docs
- Reuse terminology from `docs/ARCHITECTURE_V0.md`
- For sandbox integration, use `SandboxTemplate`, `SandboxClaim`, and `sandbox-router` terms consistently
