# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Current Architecture

TinyClaw is now organized around the Core Model:

- `internal/core`: Room, Message, Invocation, Delivery, and Trigger Policy.
- `internal/storage`: PostgreSQL persistence for the Core Model.
- `internal/api`: HTTP adapter for inbound messages, invocation completion/failure, and delivery polling/ack.
- `channel/wecom/`: legacy WeCom SDK helpers and clients.

The old in-repo TypeScript agent runtime, sandbox orchestrator, and `RoomChat` gRPC bridge have been removed. Do not reintroduce sandbox or agent execution code unless a new sandbox design is explicitly accepted.

## Build And Test

```bash
go test ./...
go build -o tinyclaw .
```

The WeCom Finance SDK native implementation only builds on Linux. Other platforms use the unsupported stub.

## Key Files

- `main.go`: service wiring
- `config.go`: environment configuration
- `control_api.go`: legacy jobs/media HTTP routes plus Core API route mounting
- `internal/core/model.go`: Core Model types
- `internal/core/trigger_policy.go`: Trigger Policy logic
- `internal/storage/core.go`: Core Model storage operations
- `internal/api/core.go`: Core Model HTTP routes

## Conventions

- Commit format: `<type>: <summary>`
- Keep one logical change per commit
- Prefer updating existing docs over creating overlapping docs
- Use the vocabulary in `CONTEXT.md`
