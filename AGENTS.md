# AGENTS.md — ragamuffin

Ragamuffin is a Go knowledge tool for agents. RAG-first, REST-native,
zero-dependency binary.

## Before You Start

1. Read [SPEC.md](SPEC.md). That's the ground truth.
2. Read the version spec for whatever you're building (e.g., `SPEC-v0.2.md`).
3. Read [CONTRIBUTING.md](CONTRIBUTING.md) for the staged-branch workflow.
4. Read [ENGINEERING-STANDARD.md](ENGINEERING-STANDARD.md) — the non-negotiable
   bar for error recovery, UX states, and accessibility on every change.
5. CI handles all build verification — do not run `go build` or `go test` locally.

## Branch Topology

The ragamuffin repo uses a three-tier staged-branch workflow:

```
dev/*  ──(PR)──→  testing  ──(PR)──→  main
                       │                  │
                 :rolling image     :latest image
                 pre-release        production
```

- **Feature branches (`dev/*`)** — branch from `testing`, PR into `testing`.
- **`testing`** — integration/staging branch. Merges build `:rolling` Docker tag.
- **`main`** — stable release branch. Merges trigger benchmark gauntlet and `:latest`.

## CI Workflow Awareness

| Workflow | Trigger | Scope |
|---|---|---|
| `pr-check.yml` | PR to `testing` | `go build`, `go test -short`, `go vet` |
| `testing-push.yml` | Merge to `testing` | Build `:rolling`, deploy, smoke tests |
| `build.yml` | Merge to `main` | Full benchmark gauntlet, `:latest`, release tag |

## Review Targets

- **robot reviews PRs against `testing`** — not `main`. Check that the PR
  branches from `testing`, targets `testing`, and follows conventions.
- **Review checklist**: `go build ./...`, `go test ./internal/... -short`,
  convention compliance, no new external dependencies beyond allowed list.

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
  vault/                  # per-agent vault lifecycle, provisioning
  session/                # session ingest and recall dispatch

plugins/
  memory-ragamuffin-openclaw/  # OpenClaw plugin adapter (Node.js) — MCP client
adapters/
  hermes-memory/               # Hermes memory adapter (Python)

sdks/
  ragamuffin-client-js/        # Node.js MCP client SDK (zero-dep)
  ragamuffin-client-py/        # Python MCP client SDK (zero-dep)

docs/
  integration/
    memory-provider-api.md     # MCP-first integration guide with 33-tool catalog
```

## Coding Conventions

### Go Style

- Standard library first. The only external dependencies are the Qdrant gRPC
  client (`github.com/qdrant/go-client`) and `modernc.org/sqlite` (log store).
  Everything else is `net/http`.
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

- `FROM alpine:3.21` in the final stage (CA certificates for HTTPS embedding APIs, `wget` for Docker healthcheck). `FROM golang:1.25-alpine` in the build stage. `CGO_ENABLED=0` for a static binary.
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

Previous version specs are archived in `docs/specs/archive/`. The current version is v0.9.6.

| Version | Status |
|---|---|
| v0.1–v0.4 | Done — foundation, multi-tenancy, auth, graph, CloudEvents |
| v0.5 | Done — fact lifecycle, memory pruning, review queue |
| v0.6 | Done — session management, per-vault fact isolation, configurable embedding dims |
| v0.7 | Done — fact graph, webhook notifications, versioned supersede, MCP foundation |
| v0.8 | Done — tiered recall, review queue dashboard, MCP-to-REST adapter, fact extraction |
| v0.9 | Done — OIDC auth, 33-tool MCP surface, session-end notifications, SDK packages |
| v0.9.6 (current) | In progress — MCP expansion, auto-provisioning, OpenClaw MCP rewrite, conformance tests |

The [ROADMAP.md](ROADMAP.md) has the high-level vision and out-of-scope list.

### Integration docs

Adapter authors and operators should read:
- [docs/integration/memory-provider-api.md](docs/integration/memory-provider-api.md) — MCP-first integration guide with 33-tool catalog

## Implementation Order

Recent versions are tracked in [CHANGELOG.md](CHANGELOG.md). For current work, follow the feature order in the relevant SPEC or ROADMAP item.

## Non-Goals

Do not add:
- Dependencies beyond the Qdrant gRPC client and `modernc.org/sqlite` (log store)
- Anything in the [ROADMAP.md](ROADMAP.md) "Out of Scope (Forever)" section

If you're unsure whether something is in scope, check the version spec.
If it's not there, it's not in scope.
