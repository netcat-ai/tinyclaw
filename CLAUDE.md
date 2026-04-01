# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## What is tinyclaw

Cloud-based AI Agent Runtime for WeChat Work (企业微信).

Current architecture:
- `clawman` pulls encrypted messages from the WeChat Work Finance SDK.
- `clawman` splits ingest and dispatch into separate loops; `messages.seq` is the archive checkpoint.
- `clawman` manages `SandboxClaim` resources directly.
- room sandboxes connect back to `clawman` over gRPC and receive `messages[]` batches.
- The sandboxed `agent` exposes `/healthz` for probes and processes messages through the gRPC bridge.
- All pulled archive items are persisted to PostgreSQL `messages`; after sandbox success, `clawman` writes reply tasks into PostgreSQL `jobs`, and marks the corresponding messages `done` only after the enqueue succeeds.

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
  -> SandboxClaim ensure
  -> sandbox gRPC client connects to clawman
  -> clawman sends messages[] batches
  -> PostgreSQL jobs(seq/client_id/recipient_alias/message/max_seq)
  -> Android WeCom sender app
```

Key files:
- `main.go` — main service entry point
- `clawman.go` — ingress pull loop, WeCom metadata resolution, sandbox invocation
- `grpc_gateway.go` — clawman gRPC server and room stream registry
- `sandbox/orchestrator.go` — direct `SandboxClaim` lifecycle manager
- `agent/src/main.ts` — sandbox runtime entry
- `agent/src/server.ts` — `/healthz`
- `agent/src/grpc.ts` — sandbox gRPC client bridge
- `agent/src/runtime.ts` — echo / `claude_agent_sdk`

## Minimal PostgreSQL tables

| Table | Purpose |
|---------|---------|
| `messages` | Inbound archive facts, status machine, and `seq` checkpoint source |
| `jobs` | Outbound reply tasks polled by the Android sender app |
| `wecom_app_clients` | Client credentials for the control API |

Short-lived WeCom detail caching and sandbox session reuse are process-local in the current single-replica version.

## Conventions

- Commit format: `<type>: <summary>` (for example `docs: clarify sandbox claim flow`)
- One logical change per commit
- Prefer updating existing docs over creating overlapping docs
- Reuse terminology from `docs/ARCHITECTURE_V0.md`
- For sandbox integration, use `SandboxTemplate` and `SandboxClaim` terms consistently
