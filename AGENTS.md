# AGENTS.md — ragamuffin

Ragamuffin is a Go knowledge tool for agents. RAG-first, REST-native,
zero-dependency binary. It turns a directory of files into a queryable
knowledge base that agents can read from and write to.

## Current State: v0.2.2

All endpoints are shipping. PRs target `main`.

| Area | Status |
|---|---|
| Semantic search (`/recall`, `/ask`) | Done |
| Write-back (`/draft`, direct + PR mode) | Done |
| Vault audit (`/audit`) | Done |
| Structured facts (`/v1/facts` POST/GET/DELETE) | Done |
| Structured logs (`/v1/logs` POST/GET) | Done |
| Snapshot (`/v1/snapshot`, streaming gzip tarball) | Done |
| MCP SSE transport (`/mcp`) | Done |
| Rate limiting (per-endpoint configurable) | Done |
| Request ID tracing | Done |
| Prometheus metrics (`/metrics`) | Done |
| Health / Stats / Version | Done |
| Watcher (poll + inotify) | Done |
| Local embeddings (via upstream config) | Done |
| Chunk size enforcement | Done |

## Project Structure

```
cmd/ragamuffin/          # entry point — wires everything up
internal/
  config/                 # env var parsing, validation, defaults
  chunker/                # markdown chunking by heading boundaries
  embedding/              # OpenAI-compatible embedding client
  llm/                    # LLM client for /ask and semantic conflict
  qdrant/                 # Qdrant gRPC client (search, scroll, upsert)
  logstore/               # SQLite-backed append-only log stream
  tokenutil/              # Token estimation utilities
  ratelimit/              # Per-endpoint rate limiter
  watcher/                # file system polling + inotify (Linux)
  indexer/                # chunking, embedding generation, Qdrant upsert
  server/                 # HTTP handlers, MCP bolt-on, audit, PR logic
    server.go             # routing, middleware, common helpers
    handlers.go           # /recall, /ask, /draft, /audit, /health, /stats
    facts.go              # /v1/facts POST/GET/DELETE
    logs.go               # /v1/logs POST/GET
    snapshot.go           # /v1/snapshot
    mcp_handlers.go       # MCP tool implementations (recall, ask, draft, audit)
    path.go               # safeVaultPath (symlink-safe path resolution)
    audit.go              # staleness, gap, duplicate, semantic conflict checks
    pr.go                 # GitHub PR creation via REST API
    audit_test.go         # audit unit tests
    handlers_test.go      # handler unit tests
  git/                    # GitHub/GitLab/Gitea provider client
  mcp/                    # MCP SSE transport + JSON-RPC dispatch (stdlib, no SDK)
```

## Before You Start

1. Read this file.
2. Read the online docs (README.md) for endpoint reference and config.
3. Run `go build ./... && go test ./... && go vet ./...` to confirm baseline.

## Coding Conventions

### Dependencies

External dependencies are minimal and all indirect:

- `github.com/qdrant/go-client` — Qdrant gRPC client
- `modernc.org/sqlite` — pure Go SQLite driver (no CGo)
- `golang.org/x/net`, `golang.org/x/text` — stdlib supplements
- `google.golang.org/grpc` + protobuf — gRPC transport for Qdrant

No web framework. No ORM. No MCP SDK. No LLM SDK. No config library.
Everything is net/http, encoding/json, database/sql, and the Go standard
library.

### Go Style

- Standard library first. Everything else is a Qdrant client or SQLite.
- Errors are wrapped: `fmt.Errorf("what failed: %w", err)`. Never discard
  an error without at least logging it.
- Contexts are passed through. Every I/O operation takes a context.
- All handlers use `writeError(w, status, code, message)` and `writeJSON`.
- `slog` for all logging. Structured, JSON to stderr. Use `logger.Info`,
  `logger.Warn`, `logger.Error`. No `fmt.Println`.

### HTTP Handlers

Every handler follows the same pattern:

```go
func (s *Server) handleFoo(w http.ResponseWriter, r *http.Request) {
    // 1. Method guard (if not a multi-method endpoint)
    if r.Method != http.MethodPost { ... }

    // 2. Body limit
    r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

    // 3. Decode
    var req fooRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil { ... }

    // 4. Validate
    if req.Field == "" { ... }

    // 5. Execute with context
    ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
    defer cancel()
    result, err := s.something(ctx, req)
    if err != nil { ... }

    // 6. Respond
    writeJSON(w, 200, result)
}
```

When a single path handles multiple methods (like `/v1/facts`), dispatch
via a switch statement:

```go
func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodPost: s.handleFactsPost(w, r)
    case http.MethodGet:  s.handleFactsGet(w, r)
    case http.MethodDelete: s.handleFactsDelete(w, r)
    default: writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET, POST, or DELETE")
    }
}
```

Routes are registered in `server.go`:

```go
mux.HandleFunc("/v1/facts", s.withRequestID(s.withRateLimit("/v1/facts", s.handleFacts)))
```

### Testing

- Test files live alongside the package: `internal/config/config_test.go`.
- Pure functions use table-driven tests with `t.Run()` for subtests.
- Handler tests use `httptest.NewRequest` + `httptest.NewRecorder`.
- No mock frameworks. If you need a real Qdrant for integration tests,
  write the test with a `t.Skip("needs integration setup")` guard.
- Go build/test/vet must pass before PR.

### Docker

- `FROM golang:1.25-alpine` for build → `FROM alpine:3.21` for runtime.
- `CGO_ENABLED=0` at build time.
- Image name: `chezgoulet/ragamuffin` (Docker Hub).
- Version injected via `-ldflags`:
  ```
  -ldflags="-s -w \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.Version=${VERSION}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.Commit=${COMMIT}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.BuildDate=${BUILD_DATE}' \
    -X 'github.com/chezgoulet/ragamuffin/internal/server.GoVersion=go1.25.0'"
  ```

## Building

```bash
# Compile, test, vet — must all pass
go build ./...
go test ./...
go vet ./...

# Docker
docker build -t chezgoulet/ragamuffin:0.2 .
```

## Configuration

All configuration is via environment variables. The `config` package
handles parsing in `internal/config/config.go`. Required vars:

| Env Var | Purpose |
|---|---|
| `RAGAMUFFIN_VAULT_PATH` | Path to the vault directory |
| `RAGAMUFFIN_QDRANT_URL` | Qdrant gRPC endpoint |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | Embedding service API key |

Full reference: README.md (Configuration section).

## Version Specs

The spec chain is:
- **README.md** — living documentation of current state
- **SPEC.md** — v0.1 architecture (historical)
- **SPEC-v0.2.md** — v0.2 additions: metrics, version, rate limiting, local embeddings (historical)
- **SPEC-v0.3.md** — this file is actually the v0.4 (Federated Knowledge) design. The actual v0.3 features (facts, logs, snapshot) were never separately spec'd — they went straight to code.
- **ROADMAP.md** — future direction

## Non-Goals

Do not add:
- Web UI (frontends are someone else's problem)
- Authentication (trust the network — agents are internal)
- Multi-tenancy
- A Python SDK
- Support for non-text files (PDF, images, audio)
- Anything in the ROADMAP's "Out of Scope (Forever)" section
