# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Current Architecture

TinyClaw is now organized around the Core Model:

- `internal/core`: Room, Message, Invocation, Delivery, and Trigger Policy.
- `internal/storage`: PostgreSQL persistence for the Core Model.
- `internal/api`: HTTP adapter for inbound messages, invocation completion/failure, and delivery polling/ack.
- `internal/executor`: in-process Invocation execution module and runner adapters.
- `channel/wecom/`: legacy WeCom SDK helpers and clients.

The old in-repo TypeScript agent runtime, sandbox orchestrator, `RoomChat` gRPC bridge, `jobs`, and `wecom_app_clients` paths have been removed. Current execution is through the Core Model Invocation execution module. `AGENT_RUNNER=codex` enables the local Codex CLI runner; sandbox/tool-runtime work remains a separate future design.

## Build And Test

```bash
go test ./...
go build -o tinyclaw .
```

The WeCom Finance SDK native implementation only builds on Linux. Other platforms use the unsupported stub.

## Key Files

- `main.go`: service wiring
- `config.go`: environment configuration
- `internal/core/model.go`: Core Model types
- `internal/core/trigger_policy.go`: Trigger Policy logic
- `internal/storage/core.go`: Core Model storage operations
- `internal/api/core.go`: Core Model HTTP routes
- `internal/executor/codex_runner.go`: Codex CLI Invocation runner

## Conventions

- Commit format: `<type>: <summary>`
- Keep one logical change per commit
- Prefer updating existing docs over creating overlapping docs
- Use the vocabulary in `CONTEXT.md`
