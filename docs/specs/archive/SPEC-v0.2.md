# Ragamuffin v0.2 — Local-First & Production-Ready

Extends [SPEC.md](SPEC.md) (v0.1). All v0.1 endpoints and behaviors remain
unchanged unless explicitly noted below.

## Overview

v0.2 makes Ragamuffin run without internet dependencies and survive in
production. The three pillars:

1. **Local embedding inference** — run entirely offline, no API credits burned
2. **Chunk size enforcement** — oversized chunks split at paragraph boundaries
3. **Production hardening** — rate limiting, metrics, version, request tracing

Zero breaking changes to the v0.1 API. Every v0.1 curl command still works.

---

## New Endpoint: `/version` — GET

Returns build information. No parameters.

**Response:**
```json
{
  "version": "0.2.0",
  "commit": "abc1234",
  "build_date": "2026-05-15T00:00:00Z",
  "go_version": "go1.25.0"
}
```

Build info is injected at compile time via `-ldflags`. If not set, fields
return `"unknown"`.

---

## New Endpoint: `/metrics` — GET

Prometheus-compatible metrics endpoint. Plain text format.

```
# HELP ragamuffin_requests_total Total HTTP requests by endpoint and status.
# TYPE ragamuffin_requests_total counter
ragamuffin_requests_total{endpoint="/recall",status="200"} 1423
ragamuffin_requests_total{endpoint="/recall",status="400"} 12
ragamuffin_requests_total{endpoint="/ask",status="200"} 89

# HELP ragamuffin_request_duration_seconds Request latency histogram.
# TYPE ragamuffin_request_duration_seconds histogram
ragamuffin_request_duration_seconds_bucket{endpoint="/recall",le="0.1"} 342
ragamuffin_request_duration_seconds_bucket{endpoint="/recall",le="0.5"} 1201
ragamuffin_request_duration_seconds_bucket{endpoint="/recall",le="1.0"} 1423
ragamuffin_request_duration_seconds_bucket{endpoint="/recall",le="+Inf"} 1423

# HELP ragamuffin_indexed_files Number of files in the index.
# TYPE ragamuffin_indexed_files gauge
ragamuffin_indexed_files 247

# HELP ragamuffin_indexed_chunks Total chunks in the index.
# TYPE ragamuffin_indexed_chunks gauge
ragamuffin_indexed_chunks 1893

# HELP ragamuffin_qdrant_health Qdrant connectivity (1 = healthy, 0 = down).
# TYPE ragamuffin_qdrant_health gauge
ragamuffin_qdrant_health 1

# HELP ragamuffin_embedding_calls_total Total embedding API calls.
# TYPE ragamuffin_embedding_calls_total counter
ragamuffin_embedding_calls_total 4521

# HELP ragamuffin_llm_calls_total Total LLM API calls.
# TYPE ragamuffin_llm_calls_total counter
ragamuffin_llm_calls_total 89
```

If Prometheus isn't configured (no scraper), `/metrics` still returns data.
It's just not collected. The endpoint has no dependencies.

---

## Changed Behavior: Rate Limiting

All endpoints are now rate-limited. The default limits are generous — a
single well-behaved agent won't hit them. A buggy loop will.

Configuration:

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_RATE_LIMIT_RECALL` | `60` | Requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_ASK` | `10` | Requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_DRAFT` | `30` | Requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_AUDIT` | `5` | Requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` | `true` | Set to `false` to disable all limits |

The rate limiter is a per-endpoint token bucket. When a limit is hit, the
endpoint returns:

```json
{
  "error": true,
  "code": "RATE_LIMITED",
  "message": "Too many requests to /recall. Limit: 60/min. Retry after: 2026-05-10T12:00:02Z"
}
```

Status code: `429 Too Many Requests`. The `Retry-After` header is also set.

Health, stats, version, and metrics endpoints are never rate-limited.

---

## Changed Behavior: Request Tracing

Every request now carries a request ID:

- **Inbound:** If the client sends `X-Request-ID`, Ragamuffin uses it.
  Otherwise, a new UUID is generated.
- **Outbound:** The request ID is included in every log line for that request.
- **Response:** `X-Request-ID` is echoed in the response headers.

Log format (JSON):

```json
{"time":"2026-05-10T12:00:00Z","level":"INFO","msg":"recall","request_id":"a1b2c3d4","endpoint":"/recall","query":"contractor rates","results":8,"duration_ms":142}
```

This makes it possible to trace a single request through structured log
queries: `jq 'select(.request_id=="a1b2c3d4")'`.

---

## New Feature: Local Embedding Proxy

A thin sidecar container that relays embedding requests to a local inference
server (Ollama, llama.cpp, any OpenAI-compatible endpoint). Ragamuffin itself
doesn't change — it already supports any OpenAI-compatible embedding API via
`RAGAMUFFIN_EMBEDDING_BASE_URL`.

### Architecture

```
┌──────────┐     ┌──────────────────┐     ┌──────────────┐
│ragamuffin│────▶│ embedding-proxy  │────▶│ ollama serve  │
│          │     │ (thin Go binary) │     │ llama-server  │
│  :8000   │     │ :8001            │     │ :11434        │
└──────────┘     └──────────────────┘     └──────────────┘
```

The embedding proxy:
- Accepts OpenAI-compatible `/v1/embeddings` requests
- Forwards to the configured backend
- Translates between API formats if needed (Ollama uses a slightly different
  shape)
- Caches nothing — stateless relay
- Exposes `/health` for Docker healthcheck

### Configuration

The embedding proxy is configured via its own env vars in docker-compose:

| Env var | Default | Notes |
|---|---|---|
| `PROXY_LISTEN` | `:8001` | Listen address |
| `PROXY_BACKEND_URL` | `http://ollama:11434` | Backend inference server |
| `PROXY_BACKEND_TYPE` | `openai_compatible` | `openai_compatible` or `ollama` |
| `PROXY_MODEL` | `nomic-embed-text` | Model name to request from backend |

Ragamuffin's config doesn't change. Point `RAGAMUFFIN_EMBEDDING_BASE_URL` at
the proxy:

```env
RAGAMUFFIN_EMBEDDING_BASE_URL=http://embedding-proxy:8001/v1
RAGAMUFFIN_EMBEDDING_API_KEY=
RAGAMUFFIN_EMBEDDING_MODEL=nomic-embed-text
RAGAMUFFIN_EMBEDDING_DIMS=768
```

Note the new `RAGAMUFFIN_EMBEDDING_DIMS` env var. Local models have different
dimensions (nomic-embed-text = 768, bge-large = 1024, etc.). When set,
Ragamuffin uses this value for Qdrant collection creation instead of the
hardcoded 1536.

### Recommended Models

| Model | Dimensions | Memory | Notes |
|---|---|---|---|
| `nomic-embed-text` | 768 | ~500 MB | Good default. Fast, small. |
| `bge-large-en-v1.5` | 1024 | ~1.3 GB | Higher quality, larger. |
| `mxbai-embed-large` | 1024 | ~1.3 GB | Strong on retrieval benchmarks. |

All available via `ollama pull <model>`.

---

## New Feature: Chunk Size Enforcement

v0.1 chunks markdown by headings regardless of chunk size. A 3,000-token
section under one heading becomes one chunk. v0.2 enforces a configurable
maximum.

### Algorithm

1. Chunk normally by heading boundaries (v0.1 behavior).
2. For each resulting chunk, estimate token count: `words × 1.3`.
3. If the chunk exceeds `RAGAMUFFIN_CHUNK_MAX_TOKENS`, split it at the
   nearest paragraph boundary (double newline) below the limit.
4. Each split inherits the parent chunk's heading as a prefix.
5. If no paragraph boundary exists within the chunk (e.g., a single
   paragraph that's too long), split at a sentence boundary (period +
   space). If that also fails, hard-split at the token limit.

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_CHUNK_MAX_TOKENS` | `2000` | Maximum tokens per chunk. 0 = unlimited (v0.1 behavior). |

This is a one-line env var change for most vaults. The default 2K tokens is
~1,500 words — a typical markdown section is 200–500 words, so most vaults
won't see any splitting.

---

## New Feature: Native File Watcher

v0.1 polls the vault directory for changes. v0.2 adds an inotify-based
watcher for Linux host mounts. Polling remains the default (works everywhere).
Inotify is opt-in.

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_WATCHER_MODE` | `poll` | `poll` or `inotify` |

Inotify mode uses `golang.org/x/sys/unix` for inotify syscalls. No new
dependencies beyond what's already in the Go standard library extended
packages.

### Edge Cases Handled

- **Directory creation:** New subdirectories are watched recursively.
- **Symlinks:** Followed if they point within the vault. Ignored if they
  point outside (security boundary).
- **Mount points:** Each mount point within the vault gets its own watch.
- **Watch exhaustion:** If the system runs out of inotify watches (common
  with large vaults), the watcher logs a warning and falls back to polling
  automatically.
- **Rapid edits:** Inotify can fire multiple events per save. Events are
  coalesced with a 500ms debounce.

### When to Use Inotify vs Polling

| Situation | Recommendation |
|---|---|
| Vault on local disk | `inotify` — zero CPU when idle |
| Vault on NFS/CIFS/network mount | `poll` — inotify doesn't work reliably over network mounts |
| Vault inside a container volume | `poll` — Docker overlay mounts don't propagate inotify events |
| Very large vault (>10K files) | `poll` — inotify watch limits are per-user, typically 8K–128K |

---

## Changed: server.go Split

v0.1's `internal/server/server.go` is 1,129 lines. v0.2 splits it:

```
internal/server/
├── server.go       # struct, New(), RegisterRoutes(), error helpers
├── handlers.go     # handleHealth, handleStats, handleRecall, handleAsk,
│                   # handleDraft, handleAudit
├── audit.go        # checkStaleness, checkGaps, checkDuplicates,
│                   # checkSemanticConflicts, truncate
├── mcp.go          # mcpTools, mcpDispatch, mcpRecall, mcpAsk,
│                   # mcpDraft, mcpAudit
└── pr.go           # createPR
```

No behavior change. No API change. Purely organizational.

---

## New Feature: Config Validation

At startup, Ragamuffin validates its configuration and fails fast with a
clear error message if something is wrong.

**Validated at startup:**
- `RAGAMUFFIN_VAULT_PATH` — directory exists and is readable
- `RAGAMUFFIN_QDRANT_URL` — parseable as a URL
- `RAGAMUFFIN_EMBEDDING_DIMS` — positive integer (new in v0.2)
- `RAGAMUFFIN_WATCH_INTERVAL` — parseable as a duration
- `RAGAMUFFIN_CHUNK_MAX_TOKENS` — non-negative integer (new in v0.2)
- `RAGAMUFFIN_WATCHER_MODE` — `poll` or `inotify` (new in v0.2)
- Rate limit values — non-negative integers

**Not validated** (checked at first use):
- Embedding API key/connectivity — checked on first `/recall`
- LLM API key/connectivity — checked on first `/ask`
- Git token validity — checked on first `/draft` PR mode
- Qdrant connectivity — checked in `/health`

Startup failures exit with code 1 and a log message:

```
FATAL: RAGAMUFFIN_VAULT_PATH "/nonexistent" does not exist or is not readable
```

---

## Full Environment Variable Reference (v0.2)

New vars are marked **[NEW]**.

| Variable | Required | Default | Notes |
|---|---|---|---|
| `RAGAMUFFIN_VAULT_PATH` | yes | — | Absolute path to the vault directory |
| `RAGAMUFFIN_QDRANT_URL` | yes | — | Qdrant server URL |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | yes | — | API key. Empty string if endpoint is keyless. |
| `RAGAMUFFIN_EMBEDDING_DIMS` **[NEW]** | no | `1536` | Embedding vector dimensions |
| `RAGAMUFFIN_WATCH_INTERVAL` | no | `60s` | Poll interval for file changes |
| `RAGAMUFFIN_WATCHER_MODE` **[NEW]** | no | `poll` | `poll` or `inotify` |
| `RAGAMUFFIN_QDRANT_COLLECTION` | no | `ragamuffin` | Qdrant collection name |
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | no | `openai` | Provider identifier |
| `RAGAMUFFIN_EMBEDDING_MODEL` | no | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | no | `https://api.openai.com/v1` | Base URL |
| `RAGAMUFFIN_CHUNK_MAX_TOKENS` **[NEW]** | no | `2000` | Max tokens per chunk. 0 = unlimited. |
| `RAGAMUFFIN_LLM_PROVIDER` | no | — | `openai_compatible` or `anthropic` |
| `RAGAMUFFIN_LLM_BASE_URL` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_LLM_MODEL` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_LLM_API_KEY` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_PORT` | no | `8000` | HTTP listen port |
| `RAGAMUFFIN_HOST` | no | `0.0.0.0` | HTTP listen address |
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` **[NEW]** | no | `true` | Enable rate limiting |
| `RAGAMUFFIN_RATE_LIMIT_RECALL` **[NEW]** | no | `60` | /recall requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_ASK` **[NEW]** | no | `10` | /ask requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_DRAFT` **[NEW]** | no | `30` | /draft requests per minute |
| `RAGAMUFFIN_RATE_LIMIT_AUDIT` **[NEW]** | no | `5` | /audit requests per minute |
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | no | `false` | Enable /draft PR mode |
| `RAGAMUFFIN_GIT_PROVIDER` | no | `github` | `github`, `gitlab`, or `gitea` |
| `RAGAMUFFIN_GIT_TOKEN` | conditional | — | Required if enabled |
| `RAGAMUFFIN_GIT_BASE_URL` | no | — | Required for Gitea |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | no | `main` | Target branch for PRs |
| `RAGAMUFFIN_GIT_REPOS` | conditional | — | Comma-separated: `owner/repo` |
| `RAGAMUFFIN_AUDIT_SAMPLE_SIZE` | no | `50` | Chunk pairs to LLM-compare |
| `RAGAMUFFIN_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

---

## Docker Compose (v0.2)

```yaml
name: ragamuffin

services:
  ragamuffin:
    image: ghcr.io/chezgoulet/ragamuffin:0.2
    # ... (v0.1 config unchanged) ...
    environment:
      RAGAMUFFIN_EMBEDDING_DIMS: ${RAGAMUFFIN_EMBEDDING_DIMS:-1536}
      RAGAMUFFIN_CHUNK_MAX_TOKENS: ${RAGAMUFFIN_CHUNK_MAX_TOKENS:-2000}
      RAGAMUFFIN_WATCHER_MODE: ${RAGAMUFFIN_WATCHER_MODE:-poll}
      RAGAMUFFIN_RATE_LIMIT_ENABLED: ${RAGAMUFFIN_RATE_LIMIT_ENABLED:-true}
      # ... rate limit overrides as needed ...

  qdrant:
    # unchanged

  # Optional: local embedding
  embedding-proxy:
    image: ghcr.io/chezgoulet/ragamuffin-embedding-proxy:0.2
    container_name: ragamuffin-embedding-proxy
    restart: unless-stopped
    environment:
      PROXY_LISTEN: ":8001"
      PROXY_BACKEND_URL: ${PROXY_BACKEND_URL:-http://ollama:11434}
      PROXY_BACKEND_TYPE: ${PROXY_BACKEND_TYPE:-openai_compatible}
      PROXY_MODEL: ${PROXY_MODEL:-nomic-embed-text}
    networks:
      - ragamuffin
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8001/health"]
      interval: 15s
      timeout: 5s
      retries: 3

  # Optional: Ollama for fully offline operation
  ollama:
    image: ollama/ollama:latest
    container_name: ragamuffin-ollama
    restart: unless-stopped
    volumes:
      - ollama_data:/root/.ollama
    networks:
      - ragamuffin
    # No ports — only embedding-proxy talks to it

volumes:
  qdrant_data:
  ollama_data:

networks:
  ragamuffin:
    driver: bridge
```

---

## Testing Requirements (v0.2)

- All v0.1 tests must still pass.
- New: chunker tests for oversized chunk splitting.
- New: rate limiter unit tests (token bucket behavior).
- New: `/version` and `/metrics` curl smoke tests.
- New: config validation tests for each validated field.
- New: embedding proxy integration test (spins up a mock backend).
- Coverage target: ≥ 80% on `internal/chunker/`, `internal/ratelimit/`.

---

## Breaking Changes

None. Every v0.1 curl command, env var, and response shape still works.
v0.2 is a drop-in replacement for v0.1.
