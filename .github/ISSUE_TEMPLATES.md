# v0.2 GitHub Issues

Copy each issue into GitHub Issues on `chezgoulet/ragamuffin`. All issues
get label `v0.2` plus any task-specific labels.

---

## Issue 1: server.go split (refactor)

**Labels:** `v0.2` `refactor`

**Description:**

`internal/server/server.go` is 1,129 lines. Split it into focused files
with zero behavior changes. This is a warm-up task — no new features,
no new tests needed beyond existing suite still passing.

**Spec reference:** SPEC-v0.2.md § "Changed: server.go Split"

**Files to create:**
- `internal/server/handlers.go` — `handleHealth`, `handleStats`,
  `handleRecall`, `handleAsk`, `handleDraft`, `handleAudit`
- `internal/server/audit.go` — `checkStaleness`, `checkGaps`,
  `checkDuplicates`, `checkSemanticConflicts`, `truncate`
- `internal/server/mcp.go` — `mcpTools`, `mcpDispatch`, `mcpRecall`,
  `mcpAsk`, `mcpDraft`, `mcpAudit`
- `internal/server/pr.go` — `createPR`

**Files to modify:**
- `internal/server/server.go` — keep only struct, `New()`,
  `RegisterRoutes()`, `writeError`, `writeJSON`, and request/response
  type definitions

**Acceptance criteria:**
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes — all 20 existing tests unchanged
- [ ] `go vet ./...` clean
- [ ] No imports changed, no function signatures changed
- [ ] Review: confirm no behavior diff (compare before/after with
  `go build -gcflags="-S"` or just verify tests pass identically)

---

## Issue 2: `/version` endpoint

**Labels:** `v0.2` `enhancement`

**Description:**

Add a `/version` endpoint that returns build info. This is the simplest
new endpoint — a good introduction to the handler pattern.

**Spec reference:** SPEC-v0.2.md § "New Endpoint: /version — GET"

**Files to create:** none (adds to `handlers.go`)

**Files to modify:**
- `internal/server/handlers.go` — add `handleVersion` handler
- `internal/server/server.go` — add `/version` route in `RegisterRoutes()`
- `cmd/ragamuffin/main.go` — inject build info via `-ldflags`
- `smoke_test.sh` — add `/version` curl test
- `.github/workflows/build.yml` — add `-ldflags` to `go build` steps
  (both Docker build and binary release)

**Build info variables (set via ldflags):**
```
-X main.version=0.2.0
-X main.commit=$(git rev-parse --short HEAD)
-X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)
```

**Acceptance criteria:**
- [ ] `curl http://localhost:8000/version` returns JSON with `version`,
  `commit`, `build_date`, `go_version`
- [ ] All four fields populated when built with ldflags
- [ ] Fields show `"unknown"` when built without ldflags
- [ ] `go test ./...` passes
- [ ] Smoke test entry added and passes

**Estimated effort:** Small (~30 min)

---

## Issue 3: Request ID tracing

**Labels:** `v0.2` `enhancement`

**Description:**

Add `X-Request-ID` header propagation: accept incoming IDs, generate
UUIDs when missing, echo in response headers, include in all log lines
for that request. This is a middleware pattern — build it once, apply
to all routes.

**Spec reference:** SPEC-v0.2.md § "Changed Behavior: Request Tracing"

**Files to create:**
- `internal/server/middleware.go` — `requestIDMiddleware` that:
  - Reads `X-Request-ID` from request header
  - Generates UUID if missing (use `crypto/rand`, no external dep)
  - Stores in request context
  - Sets `X-Request-ID` on response header
  - Wraps the handler

**Files to modify:**
- `internal/server/server.go` — wrap the mux or individual handlers
  with the middleware. Easiest approach: wrap `mux` itself at the
  `http.Server` level using a top-level handler that injects the
  request ID into context, then delegates to mux.
- `internal/server/handlers.go` — extract request ID from context
  in log calls. Add `request_id` field to all `slog` calls.
- `smoke_test.sh` — add request ID echo test

**Design note:** Don't modify every handler function signature.
Use `context.WithValue` — the middleware injects the ID, handlers
extract it with a helper like `requestIDFromContext(ctx)`.

**Acceptance criteria:**
- [ ] Sending `X-Request-ID: test-123` → response includes same header
- [ ] Sending no `X-Request-ID` → response includes auto-generated UUID
- [ ] Log lines for that request include `"request_id":"..."` 
- [ ] `go test ./...` passes (no handler behavior changes)
- [ ] Smoke test verifies echo behavior

**Estimated effort:** Medium (~1 hr)

---

## Issue 4: Rate limiting

**Labels:** `v0.2` `enhancement`

**Description:**

Add per-endpoint token-bucket rate limiting. Defaults are generous —
a single well-behaved agent won't hit them. A buggy loop will.

**Spec reference:** SPEC-v0.2.md § "Changed Behavior: Rate Limiting"

**Files to create:**
- `internal/ratelimit/ratelimit.go` — token bucket implementation
- `internal/ratelimit/ratelimit_test.go` — unit tests

**Files to modify:**
- `internal/config/config.go` — add rate limit env vars
- `internal/config/config_test.go` — test new defaults
- `internal/server/middleware.go` — add `rateLimitMiddleware`
- `internal/server/server.go` — wire middleware to each route
- `smoke_test.sh` — add rate limit test (hit /recall 61 times,
  verify 429 on the 61st)
- `.env.example` — document rate limit vars

**Config vars to add:**
| Env var | Default |
|---|---|
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` | `true` |
| `RAGAMUFFIN_RATE_LIMIT_RECALL` | `60` |
| `RAGAMUFFIN_RATE_LIMIT_ASK` | `10` |
| `RAGAMUFFIN_RATE_LIMIT_DRAFT` | `30` |
| `RAGAMUFFIN_RATE_LIMIT_AUDIT` | `5` |

**Token bucket design:**
- Per-endpoint buckets, keyed by endpoint path
- Refill rate: `limit` tokens per minute
- Burst: same as limit (no bursting — smooth rate)
- Bucket state in memory only (lost on restart — acceptable for v0.2)
- Health, stats, version, metrics endpoints are never rate-limited

**Acceptance criteria:**
- [ ] 60 rapid `/recall` requests → all succeed
- [ ] 61st `/recall` request → 429 with `RATE_LIMITED` code and
  `Retry-After` header
- [ ] `/health` never rate-limited
- [ ] Setting `RAGAMUFFIN_RATE_LIMIT_ENABLED=false` disables all limits
- [ ] `go test ./internal/ratelimit/` passes
- [ ] Config tests pass (defaults verified)

**Estimated effort:** Medium (~1.5 hr)

---

## Issue 5: `/metrics` endpoint

**Labels:** `v0.2` `enhancement`

**Description:**

Add a Prometheus-compatible `/metrics` endpoint exposing request counts,
latency histograms, indexer stats, and service health gauges.

**Spec reference:** SPEC-v0.2.md § "New Endpoint: /metrics — GET"

**Files to create:**
- `internal/metrics/metrics.go` — Prometheus-format metrics collector
- `internal/metrics/metrics_test.go` — format validation tests

**Files to modify:**
- `internal/server/handlers.go` — add `handleMetrics` handler
- `internal/server/server.go` — add `/metrics` route
- `smoke_test.sh` — add metrics format test (verify Prometheus format)

**Metrics to expose:**

| Metric | Type | Description |
|---|---|---|
| `ragamuffin_requests_total` | counter | Requests by endpoint + status code |
| `ragamuffin_request_duration_seconds` | histogram | Latency by endpoint |
| `ragamuffin_indexed_files` | gauge | Files in the index |
| `ragamuffin_indexed_chunks` | gauge | Total chunks |
| `ragamuffin_qdrant_health` | gauge | 1 = healthy, 0 = down |
| `ragamuffin_embedding_calls_total` | counter | Embedding API calls |
| `ragamuffin_llm_calls_total` | counter | LLM API calls |

**Design notes:**
- No external Prometheus client library. Write plain text format directly.
  Prometheus format is simple: `HELP`, `TYPE`, then lines of
  `name{labels} value`.
- Use `sync/atomic` for counters. No lock contention on hot paths.
- Latency histogram buckets: 0.01, 0.05, 0.1, 0.5, 1.0, 2.0, 5.0, 10.0
- The metrics collector lives on the Server struct so handlers can
  increment counters.

**Acceptance criteria:**
- [ ] `curl http://localhost:8000/metrics` returns Prometheus text format
- [ ] Output includes all 7 metrics listed above
- [ ] Counters increment correctly after requests
- [ ] Histogram buckets populate correctly
- [ ] `go test ./internal/metrics/` validates format
- [ ] Smoke test verifies output is parseable by Prometheus parser

**Estimated effort:** Medium (~2 hr)

---

## Issue 6: Config validation at startup

**Labels:** `v0.2` `enhancement`

**Description:**

Validate configuration at startup and fail fast with clear error messages.
Catches misconfiguration before the first request.

**Spec reference:** SPEC-v0.2.md § "New Feature: Config Validation"

**Files to modify:**
- `internal/config/config.go` — add `Validate() error` method
- `cmd/ragamuffin/main.go` — call `cfg.Validate()` after `config.Load()`,
  exit with code 1 on error
- `internal/config/config_test.go` — add validation tests

**Validations:**
- `VAULT_PATH` — directory exists and is readable (`os.Stat` + check IsDir)
- `QDRANT_URL` — parseable as URL (`net/url.Parse`)
- `EMBEDDING_DIMS` — positive integer (new var, default 1536)
- `WATCH_INTERVAL` — parseable as duration (`time.ParseDuration`)
- `CHUNK_MAX_TOKENS` — non-negative integer (new var, default 2000)
- `WATCHER_MODE` — one of `poll` or `inotify` (new var, default `poll`)
- Rate limit values — non-negative integers
- `AUTH_MODE` — if set, one of `none`, `api_key`, `jwt` (v0.4 forward-looking)

**Error format:**
```
FATAL: RAGAMUFFIN_VAULT_PATH "/nonexistent" does not exist or is not readable
```

Exit code: 1. Log level: ERROR (the slog logger is already initialized).

**Acceptance criteria:**
- [ ] Invalid `VAULT_PATH` → exits with clear message
- [ ] Invalid `QDRANT_URL` → exits with "not a valid URL"
- [ ] Invalid `WATCH_INTERVAL` → exits with "not a valid duration"
- [ ] Negative `CHUNK_MAX_TOKENS` → exits (0 = unlimited is valid)
- [ ] Invalid `WATCHER_MODE` → exits with "must be poll or inotify"
- [ ] All valid → starts normally
- [ ] Config tests cover each validation case

**Estimated effort:** Small (~45 min)

---

## Issue 7: Chunk size enforcement

**Labels:** `v0.2` `enhancement`

**Description:**

Enforce a maximum chunk size of 2,000 tokens. Oversized chunks are split
at the nearest paragraph boundary. This is the first indexer behavior
change — existing chunking tests must still pass.

**Spec reference:** SPEC-v0.2.md § "New Feature: Chunk Size Enforcement"

**Files to create:**
- `internal/chunker/chunker.go` — extracted chunking logic with
  token-aware splitting
- `internal/chunker/chunker_test.go` — unit tests for oversize splitting

**Files to modify:**
- `internal/indexer/indexer.go` — use `chunker` package instead of
  inline `chunkFile`/`chunkMarkdown`/`chunkPlain`. Remove those
  functions from indexer (they move to chunker).
- `internal/indexer/indexer_test.go` — chunking tests move to chunker
  package. Indexer tests for `New` and `Stats` stay.
- `internal/config/config.go` — add `RAGAMUFFIN_CHUNK_MAX_TOKENS` var
- `.env.example` — document new var

**Algorithm (from spec):**
1. Chunk normally by heading boundaries (existing behavior).
2. Estimate token count: `words × 1.3`.
3. If chunk exceeds `CHUNK_MAX_TOKENS`, split at nearest paragraph
   boundary (double newline) below limit.
4. Each split inherits parent chunk's heading as prefix.
5. Fallback: if no paragraph boundary, split at sentence boundary
   (period + space). If that fails, hard-split at token limit.

**Token estimation:** Count space-separated words, multiply by 1.3.
This is approximate — no tokenizer dependency. Close enough for
chunk size enforcement.

**Acceptance criteria:**
- [ ] `go test ./...` passes — all existing tests + new chunker tests
- [ ] 500-word chunk → not split (well under 2000 tokens)
- [ ] 3000-word chunk → split at paragraph boundary
- [ ] Single massive paragraph (no double-newlines) → split at
  sentence boundary
- [ ] Setting `CHUNK_MAX_TOKENS=0` → unlimited (v0.1 behavior)
- [ ] Chunker tests cover: normal, oversize-split, no-paragraph,
  edge cases (empty, whitespace-only, heading-only)
- [ ] Coverage ≥ 80% on `internal/chunker/`

**Estimated effort:** Medium (~2 hr)

---

## Issue 8: Native file watcher (inotify)

**Labels:** `v0.2` `enhancement`

**Description:**

Add an inotify-based file watcher for Linux host-mounted vaults.
Polling remains the default. Inotify is opt-in via `RAGAMUFFIN_WATCHER_MODE`.

**Spec reference:** SPEC-v0.2.md § "New Feature: Native File Watcher"

**Files to create:**
- `internal/watcher/inotify.go` — inotify-based watcher
- `internal/watcher/watcher.go` — extract `Watcher` interface
- `internal/watcher/poll.go` — rename current watcher to poll

**Files to modify:**
- `cmd/ragamuffin/main.go` — use watcher factory based on
  `cfg.WatcherMode`
- `internal/config/config.go` — add `WatcherMode` field
- `.env.example` — document `RAGAMUFFIN_WATCHER_MODE`

**Watcher interface:**
```go
type Watcher interface {
    Watch(events chan<- Event, done <-chan struct{})
}
```

Both `PollWatcher` and `InotifyWatcher` implement this interface.

**Inotify design:**
- Use `golang.org/x/sys/unix` for inotify syscalls (already in go.mod
  via transitive deps, but add explicit require)
- Recursive directory watching — add watches for new subdirectories
- 500ms debounce — coalesce rapid events from the same file
- Auto-fallback to polling on watch exhaustion (log warning)
- Skip symlinks pointing outside the vault

**Acceptance criteria:**
- [ ] `WATCHER_MODE=poll` → current behavior, no change
- [ ] `WATCHER_MODE=inotify` → zero CPU when vault is idle
- [ ] New file added → indexed within 2 seconds
- [ ] Modified file → re-indexed within 2 seconds
- [ ] Deleted file → chunks removed from Qdrant within 2 seconds
- [ ] New subdirectory created → watched automatically
- [ ] `go test ./...` passes — no regressions
- [ ] Inotify gracefully handled on non-Linux: `WATCHER_MODE=inotify`
  on macOS → logs warning, falls back to poll

**Estimated effort:** Medium-Large (~3 hr)

---

## Issue 9: Embedding proxy

**Labels:** `v0.2` `enhancement`

**Description:**

Create a thin sidecar binary that relays embedding requests to a local
inference server (Ollama, llama.cpp, any OpenAI-compatible endpoint).
Ragamuffin itself doesn't change — it already supports configurable
embedding APIs. The proxy is a separate binary in the same repo.

**Spec reference:** SPEC-v0.2.md § "New Feature: Local Embedding Proxy"

**Files to create:**
- `cmd/embedding-proxy/main.go` — entry point
- `internal/proxy/proxy.go` — relay logic
- `internal/proxy/proxy_test.go` — unit tests with mock backend
- `internal/proxy/ollama.go` — Ollama format translator
- `Dockerfile.proxy` — separate Docker build for the proxy

**Files to modify:**
- `docker-compose.yml` — add `embedding-proxy` and `ollama` services
  (both commented out as optional)
- `.env.example` — document proxy and Ollama env vars
- `.gitignore` — add `cmd/embedding-proxy/embedding-proxy`

**Proxy behavior:**
- Listens on `PROXY_LISTEN` (default `:8001`)
- Accepts OpenAI-compatible `/v1/embeddings` requests
- Forwards to `PROXY_BACKEND_URL` (default `http://ollama:11434`)
- Translates between OpenAI and Ollama formats when
  `PROXY_BACKEND_TYPE=ollama`:
  - Ollama expects `{model, prompt}` not `{model, input}`
  - Ollama returns `{embedding: [float32]}` not `{data: [{embedding: [...]}]}`
- Stateless — no caching
- Exposes `/health` for Docker healthcheck

**Config vars (proxy only, not ragamuffin):**

| Env var | Default |
|---|---|
| `PROXY_LISTEN` | `:8001` |
| `PROXY_BACKEND_URL` | `http://ollama:11434` |
| `PROXY_BACKEND_TYPE` | `openai_compatible` |
| `PROXY_MODEL` | `nomic-embed-text` |

**Docker compose additions (commented out by default):**
```yaml
  # Optional: local embedding
  # embedding-proxy:
  #   build:
  #     context: .
  #     dockerfile: Dockerfile.proxy
  #   ...

  # Optional: Ollama for fully offline operation
  # ollama:
  #   image: ollama/ollama:latest
  #   ...
```

**Acceptance criteria:**
- [ ] `go build ./cmd/embedding-proxy/` produces a working binary
- [ ] Proxy relays OpenAI-format requests correctly (unchanged)
- [ ] Proxy translates OpenAI → Ollama format correctly
- [ ] `/health` returns 200 when backend is reachable
- [ ] `/health` returns 502 when backend is down
- [ ] Proxy tests pass with mock backend
- [ ] Docker build `Dockerfile.proxy` produces a `FROM scratch` image
- [ ] Smoke test verifies proxy → mock backend roundtrip

**Estimated effort:** Medium-Large (~3 hr)
