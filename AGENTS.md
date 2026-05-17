# AGENTS.md — ragamuffin

Ragamuffin is a Go knowledge tool for agents. RAG-first, REST-native,
zero-dependency binary.

## Before You Start

1. Read [SPEC.md](SPEC.md). That's the ground truth.
2. Read the version spec for whatever you're building (e.g., `SPEC-v0.2.md`).
3. Run `go build ./...` and `go test ./...` to confirm a clean baseline.

## Project Structure

```
cmd/ragamuffin/          # entry point — wires everything up
internal/
  config/                 # env var parsing, defaults
  embedding/              # OpenAI-compatible embedding client
  llm/                    # LLM client for /ask and semantic conflict
  qdrant/                 # Qdrant gRPC client
  watcher/                # file system polling (v0.1) → inotify (v0.2)
  indexer/                # chunking, embedding generation, Qdrant upsert
  server/                 # HTTP handlers, MCP bolt-on, audit logic
  git/                    # GitHub/GitLab/Gitea PR creation
  mcp/                    # MCP SSE transport + JSON-RPC dispatch
  events/                 # CloudEvents v1.0 structs and webhook delivery
  auth/                   # API key and JWT authentication
  ratelimit/              # Per-endpoint rate limiter
  logstore/               # SQLite-backed structured log store
  vault/                  # v0.5 — per-agent vault lifecycle, provisioning
  session/                # v0.5 — session ingest and recall dispatch

plugins/
  memory-ragamuffin-openclaw/  # v0.5 — OpenClaw plugin adapter (Node.js)
  memory-ragamuffin-hermes/    # v0.5 — Hermes plugin adapter (Python)

docs/
  integration/
    memory-provider-api.md     # v0.5 — HTTP contract for adapters
```

## Coding Conventions

### Go Style

- Standard library first. The only external dependency is the Qdrant gRPC
  client (`github.com/qdrant/go-client`). Everything else is `net/http`.
- Errors are wrapped: `fmt.Errorf("what failed: %w", err)`. Never discard
  an error without at least logging it.
- Contexts are passed through. Every I/O operation takes a context.
- `interface{}` is acceptable for JSON serialization in handlers. It's not
  worth creating response structs for one-off shapes.
- `slog` for all logging. Structured, JSON to stderr. Use `logger.Info`,
  `logger.Warn`, `logger.Error`. No `fmt.Println`.

### HTTP Handlers

- All handlers follow the same pattern: decode → validate → execute → respond.
- Error responses use `writeError(w, status, code, message)`.
- Success responses use `writeJSON(w, status, data)`.
- New endpoints go in `internal/server/handlers.go`. Audit logic goes in
  `internal/server/audit.go`. MCP tool dispatch goes in
  `internal/server/mcp.go`. PR logic in `internal/server/pr.go`.

### Testing

- Test files live alongside the package: `internal/config/config_test.go`.
- Pure functions are tested with table-driven tests.
- Use `t.Run()` for subtests.
- Prefer `testcontainers-go` for integration tests that need a real Qdrant
  instance. Where integration tests aren't practical, use function-pointer-
  based mocks (see `internal/server/testutil/mock_qdrant.go`). Long-term
  goal: extract interfaces from qdrant.Client, embedding.Client, and
  llm.Client so any mock can be type-checked at compile time.
- Every new endpoint needs a curl smoke test entry in `smoke_test.sh`.

### Docker

- `FROM scratch` in the final stage. `CGO_ENABLED=0` in the build stage.
- Image name: `ghcr.io/chezgoulet/ragamuffin`.
- The docker-compose follows the project's cross-stack networking pattern.
  All containers on the internal `ragamuffin` network. No published ports
  except ragamuffin on `127.0.0.1:8000`.

## Building

```bash
go build ./...                    # compile check
go test ./...                     # run tests
go vet ./...                      # static analysis
docker build -t ragamuffin .      # build Docker image
./smoke_test.sh                   # curl smoke tests (needs running instance)
```

## Version Specs

Each version has its own spec. Always read the version spec before starting
work on that version.

| Version | Spec | Status |
|---|---|---|
| v0.1 | [SPEC.md](SPEC.md) | Done |
| v0.2 | [SPEC-v0.2.md](SPEC-v0.2.md) | Designed, not started |
| v0.3 | [SPEC-v0.3.md](SPEC-v0.3.md) | Done |
| v0.4 | [SPEC-v0.4.md](SPEC-v0.4.md) | Done — multi-tenancy, auth, graph, CloudEvents, web UI |
| v0.5 | [SPEC-v0.5.md](SPEC-v0.5.md) | In progress — agent memory backend, per-agent vaults, session ingest, harness plugin adapters |

The [ROADMAP.md](ROADMAP.md) has the high-level vision and out-of-scope list.

### Integration docs

Adapter authors and operators should read:
- [docs/integration/memory-provider-api.md](docs/integration/memory-provider-api.md) — HTTP contract, lifecycle mapping, error handling

## Implementation Order

When building a version, follow the spec's feature order. Each feature is
designed to be implemented independently — do one, test it, commit, move on.

For v0.2 specifically:

1. `server.go` split → refactor only, no behavior change
2. `/version` endpoint → simplest, good warm-up
3. Request ID tracing → middleware pattern
4. Rate limiting → `internal/ratelimit/`
5. `/metrics` endpoint → Prometheus format
6. Config validation → `config.Load()` enhancement
7. Chunk size enforcement → `internal/indexer/` chunker
8. Native file watcher → `internal/watcher/inotify.go`

## Non-Goals

Do not add:
- Dependencies beyond the Qdrant gRPC client and `modernc.org/sqlite` (log store)
- Anything in the [ROADMAP.md](ROADMAP.md) "Out of Scope (Forever)" section

If you're unsure whether something is in scope, check the version spec.
If it's not there, it's not in scope.
