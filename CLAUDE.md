# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**testcontainerd** is a Go integration-test container orchestration framework. It manages test-dependency containers (MySQL, Redis, MongoDB, Pulsar) and an optional SUT (System Under Test) process via a **daemon-client architecture**, enabling reusable, concurrent, auto-recycling test environments for `go test ./...`.

- Module: `github.com/McHarvvvy/testcontainerd`
- Go version: 1.24+
- Runtime dependency: Docker Engine

## Build & Test Commands

```bash
# Build / compile check
go build ./...

# Format
gofmt -w .

# Vet
go vet ./...

# Run unit-level tests (no build tags, no Docker needed)
go test -tags=integration_plan -run 'TestTC0[1-4]' ./test/

# Run integration tests requiring Docker
go test -tags='integration,integration_plan' -timeout=120s ./test/

# Run a single test
go test -tags='integration,integration_plan' -run TestTC05AcquireProvidesReadyRedisEndpoint -timeout=120s ./test/
```

Tests use build tags:
- `integration_plan` — tests that don't need Docker (config validation, registration checks)
- `integration,integration_plan` — tests that require a running Docker daemon (container lifecycle, lease management, SUT probing)

Docker-dependent tests call `requireDocker(t)` and skip automatically when Docker is unavailable.

## Architecture

The system runs in two modes determined by env var `TCD_MODE`:

- **Client mode** (default): discovers/auto-starts daemon, acquires lease, runs `m.Run()`, releases lease
- **Daemon mode** (`TCD_MODE=daemon`): registers containers, serves HTTP API, manages leases, idle-recycles

```
Test processes (clients) ──HTTP──▶ Daemon ──▶ Docker Engine
                                      │
                                      └──▶ SUT process (optional)
```

### Key packages

| Package | Role |
|---|---|
| `testcontainerd.go` | Entry point: `Config`, `New()`, `Run()`, mode dispatch |
| `client/` | Daemon discovery, auto-start (file lock), acquire/release/heartbeat |
| `daemon/` | HTTP server, lease store, container & SUT lifecycle, orphan cleanup, idle reaper |
| `container/` | `Registry` (name dedup, port conflict detection), `Bundle` (concurrent start, init, rollback), `Options` |
| `container/spec/` | Built-in drivers (MySQL, Redis, MongoDB, Pulsar) — auto-registered via `init()` |
| `protocol/` | HTTP API paths, request/response types, error codes |
| `constant/` | Container types, env var keys, lease TTL/heartbeat constants |
| `tcdruntime/` | `runtime.json` read/write/wait, path derivation, log file management |
| `examples/` | Full usage examples: `run.go`, `register.go`, `sut.go` |

### Critical flows

1. **Container startup**: `Bundle.StartAll` starts containers with max 4 concurrency, then runs `Init` functions sequentially. Init failure triggers full rollback.
2. **Daemon auto-start**: First client creates `.start.lock` via `O_CREATE|O_EXCL`; losers poll for `runtime.json`. Stale lock timeout: 90s.
3. **Lease lifecycle**: `POST /v1/acquire` → heartbeat every 1s (TTL=2s) → `POST /v1/release`. Daemon reaper GCs expired leases.
4. **Idle recycle**: Two-tier — SUT recycled first after `SUTBootPlan.GetIdleTTL()`, then daemon exits after `DaemonConfig.IdleTTL`.

### Cross-platform

- Unix: process groups (`Setpgid` + `kill -pgid`) in `process_control_unix.go`
- Windows: JobObject + `taskkill /T /F` fallback in `process_control_windows.go`; daemon copies test binary to avoid file-lock conflicts

## Code Conventions

- Import order: stdlib, then repo-internal, then third-party
- Constructors: `NewX` / `MustNewX`; option functions: `WithX`
- Error handling: return errors by default, `Must*` for panic variants; wrap lower-level errors with context
- Concurrency: `sync.Mutex` or `atomic` for shared state; idempotent shutdown (`sync.Once`)
- Logging: `log.Printf` for key runtime events; no sensitive data in logs
- Platform-specific code goes in build-tagged files (`*_unix.go`, `*_windows.go`)
- Comments explain "why", not "what"
