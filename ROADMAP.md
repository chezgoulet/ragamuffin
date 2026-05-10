# Ragamuffin — Phase 2+ Roadmap

This extends [SPEC.md](SPEC.md) (v0.1 MVP). The v0.1 spec defines the
foundation — this document maps the path from MVP to feature-complete.

## Philosophy

Ragamuffin is a knowledge tool for agents. Not a wiki. Not a CMS. Not a
document store. Its scope is bounded by four verbs:

1. **Read** — semantic search across the vault
2. **Understand** — synthesize answers from multiple sources
3. **Write** — contribute knowledge back to the vault
4. **Audit** — detect rot before it causes problems

Every feature in scope serves one of these four verbs. Everything else is
out of scope, no matter how useful it might be to someone.

## v0.2 — Local-First & Production-Ready

**Theme:** Run without internet dependencies. Survive in production.

### Local Embedding Inference

The single most impactful cost and latency improvement. Today every `/recall`
and indexing operation calls OpenAI. A pure-Go embedding runtime doesn't
exist yet (May 2026), so the pragmatic path is an embedding proxy sidecar
that talks to Ollama, llama.cpp, or any OpenAI-compatible local server.

```
┌──────────┐     ┌──────────────────┐     ┌──────────────┐
│ragamuffin│────▶│ embedding-proxy  │────▶│ ollama/       │
│          │     │ (thin HTTP relay)│     │ llama.cpp/etc │
└──────────┘     └──────────────────┘     └──────────────┘
```

The proxy is a separate container — Ragamuffin itself remains a single
binary. When `RAGAMUFFIN_EMBEDDING_BASE_URL` points to the proxy, embedding
calls stay local. No code changes to Ragamuffin — just a new env var for
the embedding model dimensions (not all local models are 1536-dim).

**Deliverables:**
- `embedding-proxy/` — thin Go binary, OpenAI-compatible endpoint
- Add `RAGAMUFFIN_EMBEDDING_DIMS` env var (default 1536, configurable)
- Docker Compose: optional embedding-proxy + ollama services
- Docs: model recommendations, dimension reference table

### Chunk Size Enforcement

The spec says "Maximum chunk size: 2,000 tokens. Oversized chunks are split
at the nearest paragraph boundary below the limit." This is implemented in
the chunker with a configurable cap.

**Deliverables:**
- `RAGAMUFFIN_CHUNK_MAX_TOKENS` env var (default 2000)
- Paragraph-boundary-aware splitting
- Approximate token counting (word count × 1.3 — good enough, no tokenizer
  dependency)

### Production Hardening

Seven small changes that together make Ragamuffin production-ready:

| Feature | Why |
|---|---|
| Rate limiting | One buggy agent shouldn't burn all your API credits |
| `/metrics` endpoint | Prometheus scraping for dashboards and alerts |
| `/version` endpoint | Know what's deployed without checking the container image |
| Request ID in logs | Trace a request through the system |
| Readiness probe | `/health` already works, just ensure it checks Qdrant |
| `server.go` split | 1,100 lines in one file is a maintenance hazard |
| Config validation at startup | Fail fast on misconfiguration, don't wait for first request |

**Deliverables:**
- Token-bucket rate limiter (configurable per endpoint)
- `/metrics` — request counts, latencies, indexer stats, Qdrant pool
- `/version` — build info from ldflags
- `X-Request-ID` header propagation
- `internal/server/` split into `handlers.go`, `audit.go`, `routes.go`

### Native File Watcher

Polling works across Docker mounts but wastes CPU on idle vaults. For
host-mounted vaults, inotify (Linux) or kqueue (macOS) eliminates the poll
loop. The watcher already has an interface — swap the implementation.

**Deliverables:**
- `RAGAMUFFIN_WATCHER_MODE` — `poll` (default) or `inotify`
- Inotify watcher for Linux host mounts
- Keep polling as fallback for network/CIFS mounts

### What Ships in v0.2

```
ragamuffin/
├── cmd/
│   ├── ragamuffin/        # unchanged (agent-accessible binary)
│   └── embedding-proxy/   # NEW — local embedding relay
├── internal/
│   ├── chunker/           # NEW — extracted from indexer, with token cap
│   ├── ratelimit/         # NEW — token-bucket middleware
│   ├── server/
│   │   ├── handlers.go    # split from server.go
│   │   ├── audit.go       # split from server.go
│   │   └── routes.go      # split from server.go
│   └── watcher/
│       ├── watcher.go     # interface
│       ├── poll.go        # current polling implementation
│       └── inotify.go     # NEW — inotify implementation
└── docker-compose.yml     # adds embedding-proxy service
```

---

## v0.3 — Smart Vault

**Theme:** The vault tests itself. Agents contribute complex changes.

### Structured Contradiction Detection

v0.1's semantic conflict detection uses random chunk pairs + LLM comparison.
It catches contradictions when chunks happen to be paired, but misses
structured data conflicts across unrelated sections.

v0.3 adds entity extraction: names, numbers, dates, status values. Chunks
that share entities but differ in values are flagged regardless of semantic
similarity. Example: `budget.md` says "$5,000" and `actuals.md` says "$50,000"
— even if the chunks aren't paired by Qdrant, entity reconciliation catches it.

**Deliverables:**
- Entity extractor (regex-based for numbers, dates, currency; LLM-assisted
  for names and statuses)
- Entity-level contradiction report in `/audit`
- `RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION` toggle

### Configurable Chunking Strategies

Markdown headings are a good default. Some vaults need different strategies:
legal docs split by clause markers, logs split by timestamp, code split by
function boundary. v0.3 makes chunking pluggable.

**Deliverables:**
- `RAGAMUFFIN_CHUNK_STRATEGY` — `heading` (default), `paragraph`, `fixed`
- Pluggable strategy interface
- Strategy-specific options (fixed size, paragraph overlap)

### Better Source Filtering

v0.1's `source_filter` uses Qdrant `Match_Text` (substring match). A filter
of `team/` matches `other-team/file.md`. v0.3 adds a payload index on
`source_file` with keyword matching for exact prefix filtering.

**Deliverables:**
- Qdrant payload index on `source_file`
- `Match_Keyword` prefix filter using Qdrant's range-based approach
- Backward-compatible — old filter still works, new filter is opt-in

### Multi-File Draft PRs

v0.1's `/draft` creates a PR with a single file. Agents often need to update
multiple files atomically — a fact and its cross-references, a policy and
its examples. v0.3 supports multi-file commits in a single PR.

**Deliverables:**
- `/draft` accepts `files: [{path, content}]` array
- Single commit, single branch, single PR
- Backward-compatible — single `target_path` + `content` still works

### Scheduled Auditing

v0.1 requires an agent or cron job to call `/audit`. v0.3 adds an internal
scheduler so the vault tests itself on a configurable interval. Results are
logged and optionally posted to a webhook.

**Deliverables:**
- `RAGAMUFFIN_AUDIT_SCHEDULE` — cron expression (e.g., `0 3 * * *`)
- Audit results logged to structured JSON
- Optional webhook delivery (`RAGAMUFFIN_AUDIT_WEBHOOK_URL`)

### What Ships in v0.3

```
ragamuffin/
├── internal/
│   ├── audit/
│   │   ├── staleness.go
│   │   ├── conflicts.go
│   │   └── entities.go      # NEW — entity extraction
│   ├── chunker/
│   │   ├── chunker.go        # interface
│   │   ├── heading.go        # current strategy
│   │   ├── paragraph.go      # NEW
│   │   └── fixed.go          # NEW
│   └── scheduler/            # NEW — cron-like audit scheduler
```

---

## v0.4 — Multi-Agent & Scale

**Theme:** One Ragamuffin, many teams. Shared infrastructure.

### Multi-Tenancy

v0.1 runs one vault per instance. v0.4 supports multiple vaults on a single
Ragamuffin instance, each with its own Qdrant collection, embedding config,
and access policy. Use case: a team runs Ragamuffin as shared infrastructure
and each project gets its own vault.

**Deliverables:**
- `RAGAMUFFIN_VAULTS` — comma-separated vault config: `name:path,name:path`
- Per-vault collections in Qdrant
- `/vault/{name}/recall`, `/vault/{name}/ask`, etc.
- Backward-compatible — single vault still works at root paths

### Authentication

v0.1 trusts the reverse proxy. v0.4 supports API keys and optional JWT
validation for teams that want defense in depth.

**Deliverables:**
- `RAGAMUFFIN_AUTH_MODE` — `none` (default), `api_key`, `jwt`
- Per-vault API keys (RW vs RO)
- JWT validation against a configured issuer

### Graph Knowledge Exploration

Agents don't just search — they navigate. v0.4 adds a `/graph` endpoint that
returns entity relationships extracted from the vault: which files reference
each other, which entities appear together, which topics cluster. Useful for
agents that need to understand the *structure* of knowledge, not just search it.

**Deliverables:**
- `/graph` endpoint — entity co-occurrence, file cross-references
- Entity graph computed during indexing (incremental)
- Response format: nodes + edges (compatible with graph visualization tools)

### Web UI (Read-Only)

A minimal web interface for humans to explore the vault, run searches, and
see audit results. Not a CMS — read-only. Write operations stay API-only.

**Deliverables:**
- Embedded static web UI (served by Ragamuffin binary)
- Search, browse, audit result views
- No editing, no user management, no settings

---

## Out of Scope (Forever)

These are good ideas. They are not Ragamuffin's job.

| Thing | Why Not |
|---|---|
| OpenAI-compatible `/v1/chat` endpoint | Ragamuffin serves knowledge, not models |
| Full-text search (Elasticsearch replacement) | Semantic search is the differentiator. Use Elasticsearch if you need keyword search. |
| Document generation / templating | That's an agent's job, not the knowledge store's |
| User management / RBAC / SSO | Trust the reverse proxy. Ragamuffin is infrastructure, not an application. |
| Audit logging for compliance | Use your infrastructure's logging pipeline |
| Built-in backup/restore | Qdrant snapshots + git for the vault. No need to reinvent. |
| Slack/Discord/Teams bot integration | Agents use the API. Notifications go through webhooks. |
| Mobile app | It's an API. Use anything that speaks HTTP. |
| Plugin marketplace | Plugin system, yes — plugin *marketplace*, no |
| Fine-tuning / RLHF / model training | Not a model server |
| PDF/image/video ingestion | v0.1 chunking could handle extracted text. Native binary parsing is a separate tool. |
| Migration tools from other RAG stacks | One-time migration scripts belong in the deployer's repo, not the binary |
| Rust rewrite | Go is chosen. Revisit only if both Qdrant ships an official Rust client AND Go embedding fails to materialize — two conditions unlikely to coincide. |

---

## Timeline (Aspirational)

| Version | Theme | Effort | Depends On |
|---|---|---|---|
| v0.1 | MVP | Done | — |
| v0.2 | Local-first + production | 3–4 weeks | Embedding proxy design |
| v0.3 | Smart vault | 4–6 weeks | v0.2 stable, entity extraction approach validated |
| v0.4 | Multi-agent + scale | 6–8 weeks | v0.3 stable, multi-tenancy use case confirmed |

Each version is independently shippable. No version requires a later version's
features. A team could stop at v0.2 and have a production-ready single-vault
RAG tool with local embeddings.

---

## Design Constraints (Carried Forward)

1. **curl test is the test.** Every new endpoint ships with a curl example in
   the spec. If it can't be tested with curl, the design is wrong.
2. **Write-back is first-class.** New features that consume the vault must
   also contribute to it.
3. **Zero-dependency binary.** `go build` produces a static binary. No
   runtime, no virtualenv. External services (Qdrant, embedding API, LLM)
   are configuration, not dependencies.
4. **REST is the foundation.** MCP, GraphQL, gRPC — whatever agents adopt —
   are bolt-ons on top of REST. The HTTP API is stable and versioned.
5. **Freshness is surfaced.** Every chunk carries a timestamp. Every audit
   surfaces staleness. The vault's age is visible.
