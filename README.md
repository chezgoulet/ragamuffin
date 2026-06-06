# 🧣 ragamuffin

> *noun.* A person, typically a child, in ragged, dirty clothes.
> In our case: a scrappy little knowledge tool that agents can actually use.

---

**Ragamuffin** serves two roles in one binary:

1. **Vault Knowledge Server** — point it at a directory, it watches for changes, indexes everything into [Qdrant](https://qdrant.tech), and serves a REST API that any agent can curl. No bridge. No translation layer.
2. **Agent Memory Backend (v0.6)** — plug it into OpenClaw or Hermes via a harness plugin adapter, and every agent gets isolated, persistent, cross-session memory backed by per-agent Qdrant collections. No agent discipline required.

```bash
# Vault mode: semantic search over watched directories
curl -s http://localhost:8000/recall \
  -d '{"query":"what do we know about that thing?"}'

# Agent memory mode: recall from a specific agent's private vault
curl -s -X POST http://localhost:8000/vault/agent::dev/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"what did Christopher ask about?"}'
```

Scroll down to [Two Patterns](#two-patterns) for the full picture.

## Quick Start

```bash
# 1. Qdrant — the only runtime dependency
docker run -d -p 6334:6334 qdrant/qdrant

# 2. Run Ragamuffin
docker run -d \
  -p 8000:8000 \
  -v /path/to/vault:/opt/vault:ro \
  -e RAGAMUFFIN_VAULT_PATH=/opt/vault \
  -e RAGAMUFFIN_QDRANT_URL=http://host.docker.internal:6334 \
  -e RAGAMUFFIN_EMBEDDING_API_KEY=sk-...  # optional — omit to run without /recall
  chezgoulet/ragamuffin:latest

# 3. Wait for indexing, then search
curl -s http://localhost:8000/recall \
  -d '{"query":"what should I know about this project?"}'

# Online docs: https://raw.githubusercontent.com/chezgoulet/ragamuffin/main/README.md
```

---

## Quick Start (Development)

### Prerequisites

- **Go 1.23+** (`go version`)
- **Qdrant** running locally (`docker run -d -p 6334:6334 qdrant/qdrant`)
- **Embedding API key** (OpenAI or compatible — `text-embedding-3-small` by default)

### Build from Source

```bash
git clone https://github.com/chezgoulet/ragamuffin.git
cd ragamuffin
go build -o ragamuffin ./cmd/ragamuffin
```

### Run Locally

```bash
# Set minimum config (embedding API key is optional — omit to run without /recall)
export RAGAMUFFIN_VAULT_PATH=./test-vault
export RAGAMUFFIN_QDRANT_URL=http://localhost:6334
export RAGAMUFFIN_EMBEDDING_API_KEY=sk-...

# Create a vault dir with some content
mkdir -p ./test-vault
echo '# Hello' > ./test-vault/index.md

# Start
./ragamuffin
```

### Run Tests

```bash
# Unit tests (no external dependencies)
go test ./internal/... -short

# Integration tests (require Qdrant + API keys — skipped by default)
go test ./... -run Integration -v
```

> **Note:** Integration tests are tagged `t.Skip()` by default and require
> explicit environment setup. Unit tests cover auth, chunking, config,
> events, LLM client, embedding client, Qdrant client, rate limiting,
> server handlers, and the indexer manager. The pruner package has no
> integration tests — testing is via the review queue API end-to-end.

### Dependency Audit

Ragamuffin minimizes external dependencies. Key libraries:

| Dependency | Purpose |
|---|---|
| [`github.com/qdrant/go-client`](https://github.com/qdrant/qdrant-awesome-go) | Qdrant gRPC client + protobuf types |
| [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) | Pure-Go SQLite driver (no CGo) |
| [`github.com/golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt) | JWT verification (RSA/ECDSA) |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | Unix syscall helpers for watcher |

No ORM, no web framework, no heavy SDK — the binary stays small and
self-contained.

---

## Two Patterns

Ragamuffin serves two distinct use cases. They can be used independently or together.

### Pattern 1: Vault Knowledge Server

Point Ragamuffin at a filesystem directory. It watches for changes (poll or inotify),
chunks files, embeds them, and indexes into Qdrant. Agents search the vault with
natural language queries. Optional LLM-backed answer synthesis.

```
┌──────────────────┐     ┌──────────────┐     ┌──────────────┐
│  File System     │────▶│  Ragamuffin  │────▶│  Qdrant      │
│  (vault dir)     │     │  (Go binary) │     │  collections │
│  docs/           │     │              │     └──────────────┘
│  ops/            │     │  ┌─────────┐ │
│  policies/       │     │  │ SQLite  │ │
└──────────────────┘     │  │ (logs)  │ │
                          │  └─────────┘ │
                          └──────┬───────┘
                                 │
                    ┌────────────┴────────────┐
                    ▼                         ▼
               ┌──────────┐            ┌──────────┐
               │ Agent A  │            │ Agent B  │
               │ (search) │            │ (search) │
               └──────────┘            └──────────┘
```

**Who this is for:** Teams that want agents to search shared documentation,
runbooks, policies, or codebases — any directory of files that needs to be
queryable by natural language.

### Pattern 2: Agent Memory Backend

Plug Ragamuffin into your agent harness (OpenClaw or Hermes) via a memory
plugin adapter. Every agent gets its own isolated Qdrant collection, persistent
session memory, and cross-agent recall — with zero agent discipline required.
The harness routes memory operations transparently: no `curl`, no tool calls
from the agent itself.

```
┌──────────────────┐     ┌──────────────┐     ┌──────────────────────┐
│  OpenClaw        │     │  Ragamuffin  │     │  Qdrant              │
│  (dev agent)     │────▶│              │────▶│  agent::dev          │
│  memory-ragamuffin│    │  POST /v1/   │     │  ┌────────────────┐  │
│  plugin          │     │  ingest      │     │  │ session turns  │  │
├──────────────────┤     │              │     │  │ recalled facts │  │
│  Hermes          │     │  POST /v1/   │     │  │ summaries      │  │
│  (robot agent)   │────▶│  recall      │     │  └────────────────┘  │
│  memory-ragamuffin│    │              │────▶├──────────────────────┤
│  plugin          │     │              │     │  Qdrant              │
└──────────────────┘     │  POST /v1/   │     │  agent::robot        │
                          │  recall?     │     │  ┌────────────────┐  │
                          │  vault=agent::│    │  │ session turns  │  │
   (cross-agent)          │  robot       │     │  │ recalled facts │  │
                          │             │      │  │ summaries      │  │
                          └──────────────┘     │  └────────────────┘  │
                                               └──────────────────────┘
```

**Who this is for:** Operators running multi-agent systems who need:
- **Guaranteed persistence** — the harness enforces memory writes, agents don't
  have to remember to call a tool
- **Per-agent isolation** — each agent's Qdrant collection is physically separate;
  metadata filter bugs can't leak data across agents
- **Cross-agent recall** — agent A can query agent B's memory via `agent_recall`
  (privileged tool provided by the harness)
- **Single infrastructure** — one Ragamuffin instance, one Qdrant cluster, all agents

### Side-by-side

| Dimension | Vault Knowledge Server | Agent Memory Backend |
|---|---|---|
| Data source | Filesystem directory | Session turn content from harness |
| Isolation | Per-vault Qdrant collections | Per-agent Qdrant collections (`agent::<name>`) |
| Agent setup | Curl / MCP from any agent | Plugin adapter installed in harness |
| Persistence | File watcher detects changes | Harness calls `POST /v1/ingest` each turn |
| Cross-agent | N/A | Privileged cross-agent recall tool |
| Hardware | One Ragamuffin | One Ragamuffin for all agents |

Both patterns can coexist: file-based vaults for shared knowledge, agent vaults
for session memory, all on the same Ragamuffin instance.

### Hybrid Pattern 3: Ragamuffin as Cross-Harness Bridge

Keep your existing harness memory slots (claudemem in OpenClaw, Honcho in
Hermes) while using Ragamuffin exclusively as a **cross-harness recall bridge**
and **structured fact store**. Agents get two additional tools that call
Ragamuffin's API directly — no plugin swap required.

```
┌──────────────────┐     ┌──────────────┐
│  OpenClaw        │     │  Claudemem   │ ← per-turn auto-persist (unchanged)
│  (dev agent)     │────▶│              │
│                  │     └──────────────┘
│  ragamuffin_     │
│  recall/store    ├───▶┌──────────────┐
│  (agent tools)   │     │  Ragamuffin  │ ← cross-harness bridge
└──────────────────┘     │              │
                          │  agent::dev │
┌──────────────────┐     │  agent::robot│
│  Hermes          │     │  agent::scout│
│  (robot agent)   │────▶│              │
│                  │     └──────────────┘
│  ragamuffin_     │
│  recall/store    ├───▶┌──────────────┐
│  (agent tools)   │     │  Honcho      │ ← per-turn auto-persist (unchanged)
└──────────────────┘     └──────────────┘
```

**Why run this way:**
- Zero migration — your existing memory setup stays exactly as-is
- Cross-harness recall works across the boundary: OpenClaw dev can ask what
  Hermes robot found in the last scan
- Agents write selectively — only important conclusions and shared facts go
  to Ragamuffin, not every turn
- Gentler operational path: validate Ragamuffin in production before committing
  to a full slot swap

**What it costs:**
- No auto-persistence — agents must explicitly call their store tool to write
- No auto-prefetch — Ragamuffin context won't appear in the system prompt
  automatically; agents must use recall when they need it
- Agent discipline is required — if the agent forgets to store something, it's
  lost (mitigated by the existing slot backend catching everything per-turn)

| Dimension | Full plugin (slot) | Hybrid (agent tools) |
|---|---|---|
| Harness slot | Swap to memory-ragamuffin | Keep claudemem/Honcho |
| Turn persistence | Automatic | Slot handles this |
| Cross-harness recall | Built-in | Via ragamuffin_recall tool |
| Agent writes | Zero-touch | Explicit tool calls |
| Migration risk | Swap and validate | Additive, zero-risk |
| End state | Full Ragamuffin | Ragamuffin as bridge layer |

All three patterns use the same Ragamuffin instance and the same per-agent
Qdrant collections. The difference is who calls the API — the harness plugin
or the agent itself.

---

## Write-Back Paths

Ragamuffin has three ways to write knowledge into a vault. They have different
latency profiles and are designed for different use cases:

| Path | Interface | Latency | Best For |
|---|---|---|---|
| File watcher | filesystem | seconds to minutes | Human-edited vault files |
| `POST /draft` / `ragamuffin_draft` | REST / MCP | near-real-time | Agent-proposed file edits |
| `POST /v1/ingest` / `ragamuffin_store` | REST / MCP | near-real-time | Agent-originated signals, observations |

### Path 1: File Watcher (Pattern 1)

The watcher polls or uses inotify to detect changes in a vault directory
(`ragamuffin.roots` in config). When a file is created, modified, or deleted,
the watcher re-chunks and re-embeds the affected content into the vault's
Qdrant collection.

**Use when:** A human is editing markdown files, runbooks, policies, or
documentation. The vault is the source of truth; Ragamuffin is the index.

**Don't use for:** Agent-generated signals, per-turn session content, or any
write that needs to survive a container restart without a filesystem. If the
watcher isn't the writer, you're adding latency and complexity.

### Path 2: `POST /draft` — Agent-Proposed File Edits (MCP: `ragamuffin_draft`)

Agents use `POST /draft` to create or update files in the vault. The request
includes the file path and content. Ragamuffin writes the file, and the
watcher picks it up on the next poll, chunks + indexes it.

**Use when:** An agent needs to write a document, update a runbook, or create
an artifact that should live in the filesystem vault. The agent proposes
content; Ragamuffin makes it real.

**Best practice:** Include a meaningful `source` in the request so downstream
consumers know who wrote it — e.g. `agent:news-digest` or `human:christopher`.
See [Source Labeling](#source-labeling) below.

### Path 3: `POST /v1/ingest` — Agent-Oriented Signals (MCP: `ragamuffin_store`)

Agents send structured documents (JSON with text + metadata) directly to the
vault, bypassing the filesystem entirely. The data goes straight into Qdrant
via the indexer event pipeline.

**Use when:** An agent wants to persist an observation, a synthesis result,
a log analysis, or any signal that doesn't belong in a file. Common patterns:
- "I learned that X depends on Y" — store as a fact for future recall
- "Scan of production found issue Z" — persist the summary
- Per-turn session memory (Pattern 2 — called by the harness, not the agent)

**Don't use for:** File-level documents that humans should read and edit.
Those go through `/draft` or the file watcher.

### Which Path Should My Client Use?

```
Is the content a human-editable file?
├─ Yes → Is the agent proposing it?
│        ├─ Yes → use POST /draft (ragamuffin_draft MCP tool)
│        └─ No  → use file watcher (write file to vault directory)
└─ No  → use POST /v1/ingest (ragamuffin_store MCP tool)
        Is this a harness-managed session?
        ├─ Yes → harness plugin calls ingest automatically
        └─ No  → agent calls store tool explicitly
```

### Agent Write Discipline

Agents that use `ragamuffin_store` or `ragamuffin_draft` must be explicitly
prompted to write back. Unlike Pattern 2 (harness plugin), the agent decides
*when* and *what* to persist. This means:

- **Prompting matters.** Include a "store important observations" instruction
  in the agent's system prompt, or let the agent decide autonomously based on
  conversation cues.
- **Deduplication is your friend.** Store signals with stable IDs so repeated
  calls are idempotent. Both `/v1/ingest` and `/v1/facts` support this.
- **Don't store everything.** Be selective — storing every turn transcript
  without filtering creates noise. The Pattern 2 harness plugin does this for
  you; if you're on Pattern 3 (hybrid), consider what's actually worth
  persisting.

### Source Labeling

Every write-back path supports a `source` or `source_type` field. Use it
consistently so queries can filter by provenance:

| Label | Meaning |
|---|---|
| `agent:news-digest` | Generated by the news-digest agent |
| `agent:scout` | Intelligence from the scout agent |
| `human:christopher` | Authored by Christopher |
| `human:shannon` | Authored by Shannon |
| `session` | Per-turn auto-persist from harness plugin |

### Real-Time vs. Batch Guidance

| Scenario | Recommended Approach |
|---|---|
| Home Assistant telemetry | n8n-generated summary document on a schedule → `POST /v1/ingest` |
| Agent log lines | Summary via LibreFang agent → `POST /v1/ingest` (not raw line-by-line) |
| Research notes | Agent writes markdown → `POST /draft` → watcher indexes |
| Incident postmortem | Human writes markdown → watcher picks up automatically |

---

## API Reference

### Core RAG Endpoints

#### `POST /recall` — Semantic search

```bash
curl -s http://localhost:8000/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"deployment process","top_k":10,"score_threshold":0.5,"source_filter":"ops/"}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Natural-language search query **(required)** |
| `top_k` | int | 10 | Max results (1–100) |
| `score_threshold` | float | 0.0 | Minimum similarity (0.0–1.0) |
| `source_filter` | string | — | Restrict to files under this path prefix |

**Response:**
```json
{
  "results": [
    {
      "text": "Deployment uses GitHub Actions...",
      "source_file": "ops/deploy.md",
      "header": "## Deployment",
      "chunk_index": 2,
      "score": 0.89,
      "file_last_updated": "2026-05-10T14:30:00Z"
    }
  ],
  "top_score": 0.89
}
```

#### `POST /ask` — Synthesized answer (requires LLM config)

```bash
curl -s http://localhost:8000/ask \
  -H "Content-Type: application/json" \
  -d '{"query":"What is our deployment strategy?","mode":"auto","top_k":8}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Question to answer **(required)** |
| `mode` | string | `auto` | `rag` (RAG-only), `auto` (RAG→full fallback), or `full` (load full source files) |
| `top_k` | int | 8 | RAG results to retrieve (1–50) |

Returns `mode_used` so callers can see if auto-mode chose RAG or full.

#### `POST /draft` — Write files to the vault

```bash
# Direct mode — writes immediately to the vault filesystem
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Database Schema","content":"...","target_path":"ops/schema.md","mode":"direct"}'

# PR mode — opens a git pull request (requires git config)
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Update schema","content":"...","target_path":"ops/schema.md","mode":"pr","description":"Add new table"}'

# Delete a file
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Delete","target_path":"ops/old.md","mode":"direct","delete":true}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `title` | string | — | File or PR title **(required)** |
| `content` | string | — | File content (omit or `""` if `delete=true`) |
| `target_path` | string | — | Path relative to vault root **(required)** |
| `mode` | string | `direct` | `direct` or `pr` |
| `description` | string | — | PR body (PR mode only) |
| `delete` | bool | `false` | Delete the file instead of writing |

Security: path traversal is blocked — resolved paths must stay under the vault root.

#### `POST /audit` — Vault health check

```bash
curl -s http://localhost:8000/audit \
  -H "Content-Type: application/json" \
  -d '{"stale_days":90,"checks":["stale","semantic_conflict","gap","duplicate"],"sample_size":50}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `stale_days` | int | 90 | Days since last update to flag as stale |
| `checks` | array | all | Which audit checks to run: `stale`, `semantic_conflict`, `gap`, `duplicate` (these are audit check names, not review_reasons types — see [review filter](#get-v1review--list-items-needing-attention)) |
| `sample_size` | int | 50 | Chunk pairs to LLM-compare (1–200, requires LLM) |

### Agent Memory Endpoints (v0.6)

These endpoints support the agent memory backend pattern. Harness plugin adapters
call them transparently — agents don't curl these directly. But you can, for
debugging and manual inspection.

#### `POST /vaults` — Create a new runtime vault

Creates a vault on disk at the given path. Only available in multi-tenant mode.

```bash
curl -s -X POST http://localhost:8000/vaults \
  -H "Content-Type: application/json" \
  -d '{"name":"my-app","path":"/data/vaults/my-app"}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Vault name **(required)** — lowercase alphanumeric + hyphens, 1-32 chars |
| `path` | string | — | Absolute filesystem path **(required)** |

**Response:** `HTTP 201 Created`
```json
{
  "name": "my-app",
  "path": "/data/vaults/my-app"
}
```

Returns `409 Conflict` if the vault already exists. For auto-provisioning,
use `POST /v1/ingest` with the `vault` field — the vault will be created
automatically on first ingest.

#### `POST /v1/ingest` — Index content into an agent vault

Persist session content, turn transcripts, or any text into an agent's vault.
Called by the harness plugin after each completed turn.

```bash
curl -s -X POST http://localhost:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "vault": "agent::dev",
    "documents": [
      {
        "id": "turn-2026-05-17-001",
        "text": "User asked about Hermes integration. I explained the MemoryProvider ABC patterns...",
        "metadata": {
          "source": "session",
          "agent": "dev",
          "session_id": "sess_abc123"
        }
      }
    ]
  }'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `vault` | string | — | Target vault name **(required)** |
| `documents` | array | `[]` | List of documents to index **(required)** |
| `documents[].id` | string | — | Unique doc/session ID **(required)** |
| `documents[].text` | string | — | Content text **(required)** |
| `documents[].metadata` | object | `{}` | Optional metadata for filtering |

> **Body size limit:** 10 MB (`MaxBytesReader`). Larger payloads receive
> `413 Request Entity Too Large` with error code `INVALID_REQUEST`.

**Response:**
```json
{
  "indexed": 1,
  "vault": "agent::dev"
}
```

#### `POST /vault/{name}/recall` — Semantic search across vaults

Vault-prefixed recall targets a specific vault by name. Same semantics as `/recall`
but with vault scoping. The vault name is part of the URL path, not the body.

```bash
# Recall from the calling agent's own vault
curl -s -X POST http://localhost:8000/vault/agent::dev/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"what did we decide about Qdrant isolation?","limit":5}'

# Cross-agent recall — query another agent's vault
curl -s -X POST http://localhost:8000/vault/agent::robot/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"what vulnerabilities were found?","limit":3}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `vault` | string | — | Vault to search **(required)** |
| `query` | string | — | Natural-language query **(required)** |
| `limit` | int | 10 | Max results (1–100) |
| `min_score` | float | 0.0 | Minimum similarity threshold (0.0–1.0) |

**Response:**
```json
{
  "results": [
    {
      "text": "Use physical Qdrant collection isolation, not metadata filters...",
      "score": 0.89,
      "metadata": {
        "source": "session",
        "agent": "dev",
        "session_id": "sess_abc123"
      }
    }
  ],
  "vault": "agent::dev"
}
```

#### `GET /v1/sessions` — List sessions (placeholder)

#### `GET /v1/sessions` — List sessions

Lists sessions filtered by agent or vault. Returns metadata without turn content.

```bash
# List all sessions for an agent
curl -s "http://localhost:8000/v1/sessions?agent_id=dev&limit=10"

# List sessions for a specific vault
curl -s "http://localhost:8000/v1/sessions?vault=agent::dev&limit=10"
```

**Response:**
```json
{
  "sessions": [{"id": "uuid", "vault": "agent::dev", "agent_id": "dev", "turn_count": 12, "created_at": "...", "updated_at": "..."}],
  "count": 1
}
```

#### `GET /v1/sessions/{id}` — Get a session with turns

Retrieves a session with its turn content.

```bash
curl -s "http://localhost:8000/v1/sessions/<session_id>?turns=50"
```

Returns full session metadata plus up to `turns` (default 50) most recent turns.

#### `POST /v1/sessions` — Create a session

Creates a new conversation session.

```bash
curl -s -X POST http://localhost:8000/v1/sessions \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "dev", "content": "Initial context...", "vault": "agent::dev"}'
```

`agent_id` is required. `vault` defaults to `agent::<agent_id>` if omitted.
`content` is optional — if provided, it becomes the first turn.

#### `POST /v1/sessions/{id}/turns` — Append a turn

Appends a turn to an existing session.

```bash
curl -s -X POST "http://localhost:8000/v1/sessions/<session_id>/turns" \
  -H "Content-Type: application/json" \
  -d '{"content": "User message...", "role": "user"}'
```

`role` can be `user`, `assistant`, or `system` (defaults to `user`).
Max 10 MB per turn.

#### `DELETE /v1/sessions/{id}` — Delete a session

```bash
curl -s -X DELETE "http://localhost:8000/v1/sessions/<session_id>"
```

Returns `{"status": "deleted", "id": "<session_id>"}`.

### Structured Data Endpoints (v0.3)

#### `POST /v1/facts` — Upsert a structured fact

```bash
curl -s -X POST http://localhost:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key":"deployment/url","value":"https://app.example.com","tags":["prod","staging"]}'
```

| Field | Type | Limits | Description |
|---|---|---|---|
| `key` | string | 1024 bytes | Fact key **(required)** |
| `value` | string | 64 KB | Fact value **(required)** |
| `tags` | array | optional | String tags for filtering |
| `ttl_days` | int | — | Time-to-live in days (0 = never expires, default 0). Pruner **StaleScan** marks expired facts as `needs_review`. |
| `confidence` | float | 0.0–1.0 | Optional confidence score (default 0.9). Pruner **LowConfidenceScan** flags facts below threshold. |
| `supersedes` | string | — | Key of a fact this new fact replaces. Pruner **SupersedeScan** marks the target as `superseded`. |

Key is hashed (SHA-256) → deterministic UUID is used as the Qdrant point ID. Re-inserting the same key upserts.

#### `GET /v1/facts` — Retrieve facts

```bash
# Exact key lookup
curl -s "http://localhost:8000/v1/facts?key=deployment/url"

# Search by text fragment (full-text / substring match on fact_key)
curl -s "http://localhost:8000/v1/facts?prefix=deploy"

# Filter by tag
curl -s "http://localhost:8000/v1/facts?tag=prod&prefix=deploy"

# Paginate
curl -s "http://localhost:8000/v1/facts?limit=20"
curl -s "http://localhost:8000/v1/facts?limit=20&before=<next_token>"
```

| Param | Description | Default |
|---|---|---|
| `key` | Exact fact_key match | — |
| `prefix` | Full-text/substring match on fact_key (see note) | — |
| `tag` | Exact tag keyword filter | — |
| `status` | Filter by lifecycle status (active, needs_review, superseded, rejected) | — |
| `limit` | Max results per page (1–1000) | 100 |
| `before` | Cursor from previous response `next_token` | — |

> ⚠️ `prefix=` performs Qdrant full-text/substring matching, not true prefix matching.
> A query for `prefix=user/` will also match `prefix=superuser/settings` due to Qdrant's
> tokenizer behavior. For exact prefix filtering, a Qdrant payload index with a keyword
> tokenizer would be needed.

**Response:**
```json
{
  "entries": [
    {
      "key": "deployment/url",
      "value": "https://app.example.com",
      "tags": ["prod"],
      "status": "active",
      "confidence": 0.9,
      "expires_at": "2026-11-11T12:00:00Z",
      "supersedes": "",
      "updated_at": "2026-05-15T12:00:00Z"
    }
  ],
  "next_token": "uuid-for-next-page"
}
```

#### `DELETE /v1/facts` — Delete a fact

```bash
curl -s -X DELETE "http://localhost:8000/v1/facts?key=deployment/url"
```

| Param | Description |
|---|---|
| `key` | Fact key to delete **(required)** |

#### `PUT /v1/facts` — Update a fact's TTL or status

Update an existing fact's lifecycle fields. The fact `key` is passed as a **query parameter**;
update fields go in the JSON body. Fields not sent are preserved.

```bash
# Update TTL and status
curl -s -X PUT "http://localhost:8000/v1/facts?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"ttl_days":180,"status":"active"}'
```

| Field | Type | Description |
|---|---|---|
| `ttl_days` | int | New TTL in days (0 = never expires) |
| `status` | string | One of `active`, `needs_review`, `superseded`, `rejected` |
| `tags` | array | Replace tags |
| `confidence` | float | Update confidence score |
| `conflict_resolved` | bool | Mark conflict as resolved |
| `value` | string | New value |
| `source` | string | Update source |
| `source_type` | string | Update source type |

**Response:**
```json
{"key":"deployment/url","updated":true}
```

#### `PATCH /v1/facts` — Partial fact update (bulk)

Patch individual fields on one or more facts without replacing the entire record.
Keys and updates are sent in the JSON body.

```bash
# Single fact
curl -s -X PATCH http://localhost:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"keys":["deployment/url"],"updates":{"value":"https://new.example.com"}}'

# Bulk update multiple facts
curl -s -X PATCH http://localhost:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"keys":["deployment/url","api/endpoint"],"updates":{"status":"needs_review"}}'
```

| Field | Type | Description |
|---|---|---|
| `keys` | array | Fact keys to update **(required**, min 1) |
| `updates.value` | string | New value |
| `updates.tags` | array | Replace tags |
| `updates.ttl_days` | int | Update TTL |
| `updates.status` | string | Update status (`active`, `needs_review`, `superseded`, `rejected`) |
| `updates.confidence` | float | Update confidence |
| `updates.conflict_resolved` | bool | Mark conflict resolved |
| `updates.source` | string | Update source |
| `updates.source_type` | string | Update source type |
| `updates.supersedes` | string | Update supersedes field |

**Response:**
```json
{
  "results": [
    {"key":"deployment/url","ok":true},
    {"key":"api/endpoint","ok":true}
  ]
}
```

#### `GET /v1/review` — List facts flagged for review

Returns facts with status `needs_review` — flagged by the pruner's stale scan,
conflict scan, supersede scan, or low-confidence scan. Pre-filtered to
`needs_review`. Supports additional filter params beyond `GET /v1/facts`.

```bash
# All flagged facts
curl -s http://localhost:8000/v1/review

# Filter by review reason type
curl -s "http://localhost:8000/v1/review?reason=stale"

# Filter by tag
curl -s "http://localhost:8000/v1/review?tag=prod"

# Filter by source type
curl -s "http://localhost:8000/v1/review?source_type=doc"

# Min confidence (show only facts below this threshold)
curl -s "http://localhost:8000/v1/review?min_confidence=0.5"

# Paginate
curl -s "http://localhost:8000/v1/review?limit=20"
```

**Response:**
```json
{
  "entries": [
    {
      "key": "deployment/url",
      "value": "https://old.example.com",
      "status": "needs_review",
      "confidence": 0.9,
      "review_reasons": [
        {"type": "stale", "detail": "expired on 2026-05-17T03:00:00Z"}
      ],
      "last_confirmed_at": "",
      "tags": ["prod"],
      "updated_at": "2026-05-17T03:00:00Z"
    }
  ],
  "total": 1,
  "next_token": "uuid-for-next-page"
}
```

| Param | Description | Default |
|---|---|---|
| `reason` | Filter by reason type (`stale`, `contradiction`, `supersession`, `low_confidence`) | — |
| `tag` | Filter by fact tag keyword | — |
| `source_type` | Filter by source type | — |
| `min_confidence` | Only show facts with confidence below this value | — |
| `limit` | Max results (1–100) | 50 |
| `before` | Cursor pagination (UUID from previous `next_token`) | — |

#### `POST /v1/review` — Resolve a review item

Mark a flagged fact as resolved. The fact `key` is passed as a **query parameter**;
the action and options go in the JSON body.

```bash
# Confirm — accept the fact as-is (sets status to active, increments confirmation_count)
curl -s -X POST "http://localhost:8000/v1/review?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"action":"confirm"}'

# Confirm with custom confidence
curl -s -X POST "http://localhost:8000/v1/review?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"action":"confirm","confidence":0.95}'

# Supersede — this fact is replaced by another
curl -s -X POST "http://localhost:8000/v1/review?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"action":"supersede","new_key":"deployment/v2/url","new_value":"https://v2.example.com"}'

# Reject — this fact is incorrect
curl -s -X POST "http://localhost:8000/v1/review?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"action":"reject","note":"Superseded by v2 deployment"}'

# Reclassify — prevent the scan from flagging again
curl -s -X POST "http://localhost:8000/v1/review?key=deployment/url" \
  -H "Content-Type: application/json" \
  -d '{"action":"reclassify","ttl_days":180}'
```

| Field | Type | Description |
|---|---|---|
| `action` | string | `confirm`, `supersede`, `reject`, or `reclassify` **(required)** |
| `confidence` | float | Updated confidence (action=`confirm`) |
| `new_key` | string | New fact key (action=`supersede`) |
| `new_value` | string | New fact value (action=`supersede`) |
| `note` | string | Optional reason for the resolution |
| `ttl_days` | int | Update TTL (action=`reclassify`) |
| `tags` | array | Update tags |
| `source` | string | Update source |
| `source_type` | string | Update source type |

**Response:**
```json
{"key":"deployment/url","action":"confirm","status":"active","resolved":true}
```

#### `GET /v1/review/stats` — Review queue statistics

Returns aggregate counts for the review queue, broken down by reason and source type.

```bash
curl -s http://localhost:8000/v1/review/stats
```

**Response:**
```json
{
  "total_needs_review": 12,
  "by_reason": {
    "stale": 8,
    "contradiction": 3,
    "low_confidence": 1
  },
  "by_source_type": {
    "doc": 5,
    "agent": 4,
    "unknown": 3
  },
  "oldest_item": "2026-05-01T12:00:00Z",
  "avg_pending_days": 14.5
}
```

#### `POST /v1/logs` — Append a log entry

```bash
curl -s -X POST http://localhost:8000/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"agent":"scout","type":"scan","body":"Found 3 vulnerabilities","tags":["security","npm"],"timestamp":"2026-05-15T12:00:00Z"}'
```

| Field | Type | Limits | Description |
|---|---|---|---|
| `agent` | string | 256 bytes | Agent or service identifier **(required)** |
| `type` | string | 256 bytes | Log type/category **(required)** |
| `body` | string | 64 KB | Log content **(required)** |
| `tags` | array | ≤50 entries, 256 bytes each | Optional string tags |
| `timestamp` | string | ISO 8601 / RFC 3339 | Optional; server time if omitted |

#### `GET /v1/logs` — Query log entries

```bash
# All logs for an agent
curl -s "http://localhost:8000/v1/logs?agent=scout"

# Filter by type and tag
curl -s "http://localhost:8000/v1/logs?type=scan&tag=security"

# Time range (ISO 8601 / RFC 3339)
curl -s "http://localhost:8000/v1/logs?since=2026-05-01T00:00:00Z&until=2026-05-15T00:00:00Z"

# Paginate
curl -s "http://localhost:8000/v1/logs?limit=50"
curl -s "http://localhost:8000/v1/logs?limit=50&before=<hex_cursor>"
```

| Param | Description | Default |
|---|---|---|
| `agent` | Filter by agent | — |
| `type` | Filter by type | — |
| `tag` | Exact tag filter (uses `json_each`, not `LIKE`) | — |
| `since` | Return entries after this timestamp (RFC 3339) | — |
| `until` | Return entries before this timestamp (RFC 3339) | — |
| `before` | Cursor: entries before this ID (hex rowid) | — |
| `limit` | Max results per page (1–1000) | 100 |

#### `GET /v1/snapshot` — Download vault as gzipped tarball

```bash
curl -s -O http://localhost:8000/v1/snapshot
# → vault-2026-05-15.tar.gz
```

Streaming download at `/v1/snapshot`. Best-effort consistency — files may change during the walk. Skips the `.ragamuffin/` directory (operational metadata).

### Observability Endpoints

#### `GET /health` — Service health

```bash
curl -s http://localhost:8000/health
```

Returns `200 OK` with Qdrant reachable check. Returns `200` with `status: "indexing"` during initial reindex. Returns `502` if Qdrant is unreachable.

#### `GET /stats` — Indexer metrics

```bash
curl -s http://localhost:8000/stats
```

Returns vault path, indexed file count, total chunks (from Qdrant, authoritative), last indexed time, embedding provider, uptime.

> **Multi-tenant note:** In multi-tenant mode `/v1/facts`, `/v1/logs`, and `/v1/snapshot` are **global** endpoints — they operate on the
> first-configured vault, not per-vault. Use the vault-prefixed routes
> (`/vault/{name}/v1/facts`, `/vault/{name}/v1/logs`, `/vault/{name}/v1/snapshot`)
> for per-vault access.

#### `GET /version` — Build info

```bash
curl -s http://localhost:8000/version
```

Returns version, commit hash, build date, Go version (set via `-ldflags`).

#### `GET /metrics` — Prometheus endpoint

```bash
curl -s http://localhost:8000/metrics
```

Plain-text Prometheus format with counters for requests, durations, indexed files/chunks.

#### `GET /events` — Real-time event stream (SSE)

```bash
curl -N http://localhost:8000/events
```

Opens a Server-Sent Events (SSE) stream of vault CloudEvents. Each event is a
JSON payload in CloudEvents v1.0 format. The connection stays open and pushes
events as they happen.

**SSE format:**

```
event: vault.file.changed
data: {"specversion":"1.0","type":"vault.file.changed","source":"/opt/vault",...}
```

**Event types:**

| Event | When |
|---|---|
| `vault.file.changed` | A file in the vault was created or modified |
| `vault.file.deleted` | A file in the vault was deleted |
| `vault.collection.reindexed` | A Qdrant collection was fully reindexed |
| `ragamuffin.started` | Server started (sent once at boot) |
| `ragamuffin.healthy` | Server health state change |

> **Note:** The `/events` endpoint does not require authentication — it is
> intentionally public so SSE clients can connect before tokens are obtained.
> Slow consumers may have events dropped (buffer: 64 events).

### Agent Protocol Endpoint

#### `GET /mcp` — SSE stream (long-lived connection)

Agents that support MCP connect via Server-Sent Events. Flow:
1. Client opens `GET /mcp` → receives SSE stream with session ID
2. Client sends JSON-RPC requests via `POST /mcp` (events endpoint sent in SSE `endpoint` event)
3. Server pushes tool results and notifications back through the SSE stream

```bash
# Connect (opens persistent SSE connection)
curl -s -N http://localhost:8000/mcp

# In a separate shell, send tool invocation
curl -s -X POST http://localhost:8000/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ragamuffin_recall","arguments":{"query":"deployment strategy"}}}'
```

#### Implements

| Method | Client Request | Server Response |
|---|---|---|
| `initialize` | Protocol handshake | Server info + capabilities (tools, streaming) |
| `tools/list` | List available tools | Tool definitions with input schemas |
| `tools/call` | Invoke tool by name | Tool result (JSON) pushed via SSE |
| `notifications/tools/list_changed` | (server→client) | Server pushes when tools change |

#### Available Tools

All tools mirror the REST API:
- `ragamuffin_recall` — semantic search (`/recall`)
- `ragamuffin_ask` — synthesized answer with RAG (`/ask`)
- `ragamuffin_draft` — write files to vault or create PR (`/draft`)
- `ragamuffin_audit` — vault health checks (`/audit`)

Client disconnect cancels any in-flight operations. Each SSE connection has a
40-second keepalive heartbeat. Sessions expire after 5 minutes of inactivity.

---

### v0.4 Endpoints

#### `GET /vaults` — List configured vaults (v0.4)

```bash
curl -s http://localhost:8000/vaults
```

In single-tenant mode, returns a single "default" vault. In multi-tenant mode, returns all configured vaults with status.

#### `GET /graph` — Knowledge graph (v0.4)

```bash
# Full graph
curl -s http://localhost:8000/graph

# Entity-focused
curl -s 'http://localhost:8000/graph?entity=Qdrant&depth=2'
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `entity` | string | — | Focus on a specific entity |
| `depth` | int | 1 | Graph traversal depth (1–5) |
| `limit` | int | 50 | Max nodes to return (1–200) |

Returns nodes (files and entities) and edges (contains, links_to).

#### `POST /reindex` — Full reindex (v0.4)

```bash
curl -s -X POST http://localhost:8000/reindex
```

Triggers a full re-index of the vault. Non-blocking — returns immediately and reindex runs asynchronously.

### Multi-tenant Mode (v0.4)

When `RAGAMUFFIN_VAULTS` is set, all content endpoints are prefixed with `/vault/{name}/`:

```bash
curl -s 'http://localhost:8000/vault/docs/recall?query=deploy'
curl -s 'http://localhost:8000/vault/docs/graph'
```

Available vault-prefixed endpoints:
- `/vault/{name}/recall`
- `/vault/{name}/ask`
- `/vault/{name}/draft`
- `/vault/{name}/audit`
- `/vault/{name}/v1/facts`
- `/vault/{name}/v1/logs`
- `/vault/{name}/v1/snapshot`
- `/vault/{name}/reindex`
- `/vault/{name}/graph`

### Authentication (v0.4)

Three modes controlled by `RAGAMUFFIN_AUTH_MODE`:

| Mode | Description |
|---|---|
| `none` | No authentication (default) |
| `api_key` | Static API keys from environment variables |
| `jwt` | JWT tokens validated via JWKS endpoint |

**API Key mode:**
- `RAGAMUFFIN_AUTH_READ_KEY` — global read key
- `RAGAMUFFIN_AUTH_WRITE_KEY` — global write key
- `RAGAMUFFIN_AUTH_READ_KEY_{VAULT}` — per-vault scoped read key
- `RAGAMUFFIN_AUTH_WRITE_KEY_{VAULT}` — per-vault scoped write key

**JWT mode:**
- `RAGAMUFFIN_AUTH_JWT_ISSUER` — expected JWT issuer
- `RAGAMUFFIN_AUTH_JWT_AUDIENCE` — expected audience
- `RAGAMUFFIN_AUTH_JWT_JWKS_URL` — JWKS endpoint for key discovery

JWT must include a `ragamuffin` claim with an `access` field (`read` or `read_write`).

> **Auth exemptions:** The `/events` (SSE) and `/mcp` (SSE + JSON-RPC)
> endpoints do not require authentication. SSE clients need to connect
> before obtaining tokens, and MCP uses protocol-level auth via
> `ragamuffin_store`/`ragamuffin_draft` tool calls rather than HTTP
> headers. All other endpoints require auth when a mode other than
> `none` is set.

### Web UI (v0.4)

Ragamuffin ships an embedded web UI served at the root path:
- `GET /` — SPA dashboard with Search, Browse, Audit, and Graph pages
- `GET /static/*` — Static assets (CSS, JS)

API routes take priority over static file serving.

---

## Harness Integration (v0.6)

Ragamuffin ships as the memory backend for both OpenClaw and Hermes agents.
The adapters are reference implementations — any harness with a pluggable memory
backend can adopt the same API contract.

### OpenClaw — `plugins.slots.memory = "memory-ragamuffin"`

Configure in `openclaw.json`:

```json5
{
  plugins: {
    slots: {
      memory: "memory-ragamuffin",
    },
    entries: {
      "memory-ragamuffin": {
        enabled: true,
        config: {
          endpoint: "http://ragamuffin:8000",
          vaultPrefix: "agent::",
          autoRecall: true,
          autoCapture: true,
        },
      },
    },
  },
}
```

That's it. Restart OpenClaw and every agent's memory is automatically
Ragamuffin-backed — per-agent Qdrant isolation, session persistence,
and cross-agent recall. Agents write zero code.

**Don't want to swap slots yet?** See [Hybrid: Ragamuffin as cross-harness
bridge](#hybrid-pattern-3-ragamuffin-as-cross-harness-bridge) — you can add
Ragamuffin as agent tools alongside your existing memory backend with zero
migration.

### Hermes — `memory.provider: "ragamuffin"`

Configure in `config.yaml`:

```yaml
memory:
  provider: ragamuffin
  ragamuffin:
    endpoint: "http://ragamuffin:8000"
    vault_prefix: "agent::"
```

Hermes discovers the plugin from `plugins/memory/ragamuffin/`. The adapter
implements the `MemoryProvider` ABC — `initialize`, `prefetch`, `sync_turn`,
`get_tool_schemas`, `on_session_end`, `shutdown`.

### Lifecycle mapping

Both adapters implement the same mapping from harness hooks to Ragamuffin API calls:

| Harness hook | Ragamuffin API call |
|---|---|
| Plugin load / agent start | `POST /v1/ingest` — auto-provisions vault on first ingest |
| Pre-turn recall | `POST /vault/{name}/recall` — semantic search against agent vault |
| Post-turn persist | `POST /v1/ingest` — index the completed turn |
| Session end | `POST /v1/ingest` — index a session summary artifact |
| Cross-agent recall | `POST /vault/agent::robot/recall` — query another agent's vault |

### Writing an adapter for another harness

See [docs/integration/memory-provider-api.md](docs/integration/memory-provider-api.md)
for the full HTTP contract, OpenAPI spec, error handling guide, and agent identity
conventions. The adapters are ~200 lines each — the contract is the hard part.

---

## Rate Limits

Per-endpoint rate limiting via environemnt variables. Disabled by default; enable with `RAGAMUFFIN_RATE_LIMIT_ENABLED=true`.

| Endpoint | Env Var | Default (req/min) |
|---|---|---|
| `/recall` | `RAGAMUFFIN_RATE_LIMIT_RECALL` | 60 |
| `/ask` | `RAGAMUFFIN_RATE_LIMIT_ASK` | 10 |
| `/draft` | `RAGAMUFFIN_RATE_LIMIT_DRAFT` | 30 |
| `/audit` | `RAGAMUFFIN_RATE_LIMIT_AUDIT` | 5 |
| `/v1/facts` | `RAGAMUFFIN_RATE_LIMIT_FACTS` | 30 |
| `/v1/logs` | `RAGAMUFFIN_RATE_LIMIT_LOGS` | 60 |
| `/v1/snapshot` | `RAGAMUFFIN_RATE_LIMIT_SNAPSHOT` | 5 |
| `/v1/ingest` | `RAGAMUFFIN_RATE_LIMIT_INGEST` | 30 |
| `/v1/review` | `RAGAMUFFIN_RATE_LIMIT_REVIEW` | 30 |
| `/reindex` | `RAGAMUFFIN_RATE_LIMIT_REINDEX` | 30 |

When rate-limited, responds with `429 Too Many Requests` with a `Retry-After`
header set to the number of seconds to wait (integer, not a date string).

---

## Storage

### Qdrant Collections

| Collection | Env Var | Default | Vector Size | Purpose |
|---|---|---|---|---|
| Main index | `RAGAMUFFIN_QDRANT_COLLECTION` | `ragamuffin` | 1536 (default) | File chunk embeddings for /recall |
| Facts | `RAGAMUFFIN_FACTS_COLLECTION` | `ragamuffin_facts` | 4 (configurable) | Structured key-value facts, zero-vector sentinel |

The facts collection uses a 4-dim zero vector `[0,0,0,0]` by default — payload-only storage
that satisfies Qdrant's vector requirement without embedding costs.

### SQLite Database

Ragamuffin creates a SQLite database at `<vault>/.ragamuffin/logs.db` for the structured log
store. Uses WAL mode and `synchronous=NORMAL` for concurrent access. Pure Go —
uses `modernc.org/sqlite`, no CGo dependency.

### Request Body Limits

| Endpoint | Limit |
|---|---|
| `/recall`, `/ask`, `/audit` | 64 KB |
| `/v1/facts` (POST) | 256 KB |
| `/v1/logs` (POST) | 64 KB |
| `/v1/ingest` (POST) | 10 MB |
| `/draft` | 10 MB |

---

## Fact Lifecycle (v0.6)

Ragamuffin's pruner subsystem manages the life cycle of structured facts:
what's current, what's stale, what contradicts itself, and what's been
superseded. Facts pass through four states:

| State | Meaning | Transition
|---|---|---|
| `active` | Current, trusted fact | Default on creation; may be flagged by pruner or manually via review |
| `needs_review` | Flagged by pruner — stale, low-confidence, or potentially conflicted | Pruner scans auto-set this; resolved via review queue |
| `superseded` | Replaced by newer information | Manually set via review; original value retained for audit trail |
| `rejected` | Determined to be incorrect | Manually set via review; preserved for debugging |

All state transitions preserve the original fact data. Facts are never deleted —
their state determines whether they appear in queries and searches.

### Pruner Scans

The pruner runs three scan types on independent configurable intervals.
Each scan reads facts, flags candidates, and updates their status to
`needs_review`. The pruner never deletes or modifies fact values — it marks
facts for human (or agent) review.

| Scan | What it does | Default interval |
|---|---|---|
| **StaleScan** | Flags facts past their `expires_at` | 24h |
| **LowConfidenceScan** | Flags facts with `confidence` below the configured threshold | 24h |
| **ConflictScan** | Compares fact values for the same or similar keys, flags semantic overlaps | 72h |
| **SupersedeScan** | Cross-references fact sources against vault files; flags if source has been superseded | 24h |

### Review Queue

Facts flagged by the pruner are surfaced through the review queue — a set of
REST endpoints for listing, inspecting, and resolving flagged items.

#### `GET /v1/review` — List items needing attention

```bash
curl -s 'http://localhost:8000/v1/review?reason=stale&limit=20'
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `reason` | string | — | Filter: `stale`, `conflict`, `superseded`, `low_confidence` |
| `tag` | string | — | Filter by fact tag |
| `source_type` | string | — | Filter by source type |
| `min_confidence` | float | 0.0 | Minimum confidence threshold |
| `limit` | int | 100 | Max results per page |
| `before` | string | — | Cursor pagination from previous response |

**Response:**

```json
{
  "entries": [
    {
      "key": "deployment/url",
      "value": "https://old-app.example.com",
      "confidence": 0.7,
      "expires_at": "2026-05-01T00:00:00Z",
      "review_reasons": [
        {"type": "stale", "detail": "fact expired at 2026-05-01T00:00:00Z"}
      ],
      "status": "needs_review",
      "updated_at": "2026-04-01T12:00:00Z"
    }
  ],
  "next_token": "uuid-for-next-page"
}
```

#### `POST /v1/review` — Resolve a flagged item

```bash
curl -s -X POST 'http://localhost:8000/v1/review?key=deployment/url' \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm"}'
```

| Parameter | Description |
|---|---|
| `key` | Fact key to resolve **(query param, required)** |
| `action` | Action to take: `confirm`, `supersede`, `reject`, `reclassify` **(required)** |
| `new_value` | New fact value (for `supersede`) |
| `new_tags` | Updated tags (for `reclassify`) |

| Action | Effect |
|---|---|
| `confirm` | Status → `active`, confidence boosted by configured amount |
| `supersede` | Old fact → `superseded` status; new fact created from `new_value` |
| `reject` | Status → `rejected`, original value preserved |
| `reclassify` | Tags updated to `new_tags`, status → `active` |

**Response:**

```json
{
  "key": "deployment/url",
  "status": "active",
  "action": "confirm",
  "confidence": 0.8
}
```

#### `GET /v1/review/stats` — Review queue summary

```bash
curl -s http://localhost:8000/v1/review/stats
```

**Response:**

```json
{
  "total_needs_review": 12,
  "by_reason": {
    "stale": 5,
    "low_confidence": 4,
    "conflict": 2,
    "superseded": 1
  },
  "by_source_type": {
    "agent": 8,
    "vault": 4
  },
  "oldest_item": "2026-05-01T00:00:00Z",
  "avg_pending_days": 14.3
}
```

### Fact Lifecycle Configuration

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_PRUNER_ENABLED` | `false` | Master switch for all pruner scans |
| `RAGAMUFFIN_PRUNER_STALE_INTERVAL` | `24h` | How often stale scan runs |
| `RAGAMUFFIN_PRUNER_STALE_DAYS` | `90` | Days past `expires_at` to flag as stale |
| `RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL` | `72h` | How often conflict scan runs |
| `RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE` | `50` | Number of fact pairs to compare per cycle |
| `RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL` | `24h` | How often supersede scan runs |
| `RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD` | `0.5` | Facts below this confidence are flagged |

---

## Configuration

### Required

| Env Var | Description |
|---|---|
| `RAGAMUFFIN_VAULT_PATH` | Path to the knowledge base directory |
| `RAGAMUFFIN_QDRANT_URL` | Qdrant gRPC endpoint (e.g. `http://localhost:6334`) |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | API key for the embedding service (optional unless indexing or /recall is needed) |

### Embedding

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | `openai` | Embedding API provider |
| `RAGAMUFFIN_EMBEDDING_MODEL` | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | `https://api.openai.com/v1` | API base URL (for proxies) |
| `RAGAMUFFIN_EMBEDDING_DIMS` | `1536` | Output dimensions |

### LLM

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_LLM_PROVIDER` | — | LLM provider (e.g. `openai`) |
| `RAGAMUFFIN_LLM_BASE_URL` | `https://api.deepseek.com` | API base URL without `/v1` — the LLM client appends `/v1/chat/completions` internally. For LiteLLM proxy use `http://litellm:4000`. See [URL convention](#url-conventions). |
| `RAGAMUFFIN_LLM_MODEL` | — | Model name (e.g. `gpt-4o`, `deepseek-chat`, `deepseek-v4-flash`) |
| `RAGAMUFFIN_LLM_API_KEY` | — | LLM API key |
| `RAGAMUFFIN_LLM_TIMEOUT` | `120s` | LLM request timeout (Go duration) |

### URL Conventions

Ragamuffin has two API clients with **opposite base URL conventions** — this is by design after normalization.

| Client | Appends to base URL | Example `RAGAMUFFIN_*_BASE_URL` |
|---|---|---|
| **Embedding** | `/embeddings` | `https://api.openai.com/v1` (include `/v1`) |
| **LLM** | `/v1/chat/completions` | `https://api.deepseek.com` (omit `/v1`) |

For a LiteLLM proxy (`http://litellm:4000`), set:
- `RAGAMUFFIN_EMBEDDING_BASE_URL=http://litellm:4000/v1` (LiteLLM proxies `/v1/embeddings`)
- `RAGAMUFFIN_LLM_BASE_URL=http://litellm:4000` (LiteLLM handles `/v1/chat/completions`)

### Qdrant

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_QDRANT_COLLECTION` | `ragamuffin` | Main index collection name |
| `RAGAMUFFIN_FACTS_COLLECTION` | `ragamuffin_facts` | Facts collection name |
| `RAGAMUFFIN_FACTS_VECTOR_SIZE` | `4` | Facts collection vector dimensionality |

### Watcher

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_WATCH_INTERVAL` | `60s` | Poll interval (poll mode) |
| `RAGAMUFFIN_WATCHER_MODE` | `poll` | `poll` or `inotify` (Linux only) |

### Chunking

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_CHUNK_MAX_TOKENS` | `2000` | Max tokens per chunk (0 = no limit) |

### Git

| Env Var | Description |
|---|---|
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | Enable PR mode (`true`/`false`) |
| `RAGAMUFFIN_GIT_PROVIDER` | `github` (default) |
| `RAGAMUFFIN_GIT_TOKEN` | Git provider access token |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | `main` (default) |
| `RAGAMUFFIN_GIT_BASE_URL` | API base URL (for self-hosted) |
| `RAGAMUFFIN_GIT_REPOS` | Repository list |

### Events (v0.4)

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_EVENT_WEBHOOK_URL` | — | Webhook URL for CloudEvents v1.0 (empty = disabled) |

When configured, Ragamuffin emits CloudEvents v1.0 structured JSON via HTTP POST
with `Content-Type: application/cloudevents+json`. Delivery is fire-and-forget (async).

| Event Type | When |
|---|---|
| `vault.file.changed` | File created or modified (after successful index) |
| `vault.file.deleted` | File deleted from index |
| `ragamuffin.started` | Server boot, before listen |

### Server

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_HOST` | `0.0.0.0` | HTTP listen host |
| `RAGAMUFFIN_PORT` | `8000` | HTTP listen port |
| `RAGAMUFFIN_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

All handlers are wrapped in a panic recovery middleware that logs stack traces
via slog and returns JSON 500 errors instead of silent connection drops.

### Pruner (Fact Lifecycle Management)

The Pruner runs scheduled background scans on the facts collection to manage
fact lifecycle. Each scan type can be configured independently.

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_PRUNER_ENABLED` | `false` | Master switch for all scans |
| `RAGAMUFFIN_PRUNER_STALE_INTERVAL` | `24h` | Stale scan interval (0 = disabled) |
| `RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL` | `72h` | Conflict scan interval (0 = disabled) |
| `RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL` | `24h` | Supersede scan interval (0 = disabled) |
| `RAGAMUFFIN_PRUNER_STALE_DAYS` | `90` | Days past TTL expiry before a fact is flagged stale |
| `RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE` | `50` | Pairs per conflict scan cycle |
| `RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD` | `0.5` | Below this → flagged `needs_review` |

**Scan types:**

| Scan | What it does |
|---|---|
| **StaleScan** | Queries facts with `status=active AND ttl_days>0 AND expires_at_unix<now`, marks them `needs_review` |
| **ConflictScan** | Samples active facts, embeds values, compares cosine similarity (threshold 0.85). High-similarity pairs with different values are flagged |
| **SupersedeScan** | Two sub-scans: cross-reference (facts with `supersedes` field pointing to an active target → target marked `superseded`) and key-pattern (`org/v2/x` vs `org/v1/x` → lower version marked `superseded`) |
| **LowConfidenceScan** | One-time scan at startup: marks active facts with confidence < threshold as `needs_review` |

### Tuning

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_AUDIT_SAMPLE_SIZE` | `50` | Default sample size for audit checks |
| `RAGAMUFFIN_AUTO_THRESHOLD` | `0.75` | Auto-mode RAG→full fallback threshold |
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` | `false` | Enable per-endpoint rate limiting |

---

## Error Codes

All endpoints return errors in a uniform format:

```json
{
  "error": true,
  "code": "ERROR_CODE",
  "message": "Human-readable description"
}
```

### Client Errors (4xx)

| Code | Description | HTTP Status |
|---|---|---|
| `INVALID_REQUEST` | Malformed request — missing fields, invalid JSON, wrong method | 400 |
| `INVALID_INPUT` | Required fields missing or invalid | 400 |
| `INVALID_DATA` | Corrupt or unparseable stored data | 400 |
| `INVALID_ACTION` | Invalid review queue action | 400 |
| `MISSING_KEY` | Required key parameter missing | 400 |
| `MISSING_KEYS` | Required keys array missing | 400 |
| `MISSING_ACTION` | Review action field missing | 400 |
| `KEY_TOO_LONG` | Fact key exceeds 1024 bytes | 400 |
| `VALUE_TOO_LARGE` | Fact value exceeds 64 KB | 400 |
| `TAG_TOO_LONG` | Tag string exceeds 256 bytes | 400 |
| `TOO_MANY_TAGS` | Tag array exceeds 50 entries | 400 |
| `AGENT_TOO_LONG` | Agent name exceeds 256 bytes | 400 |
| `TYPE_TOO_LONG` | Log type exceeds 256 bytes | 400 |
| `EMPTY_TAG` | Empty tag provided | 400 |
| `BODY_TOO_LARGE` | Request body exceeds endpoint limit | 413 |
| `METHOD_NOT_ALLOWED` | Wrong HTTP method for endpoint | 405 |
| `FORBIDDEN` | Insufficient access permissions | 403 |
| `NOT_FOUND` | Endpoint doesn't exist | 404 |
| `CONFLICT` | Resource already exists or is in progress | 409 |
| `INVALID_SUPERSEDE` | Supersede requires new value | 400 |
| `INVALID_SINCE` | Invalid `since` timestamp format | 400 |
| `INVALID_UNTIL` | Invalid `until` timestamp format | 400 |
| `INVALID_TIMESTAMP` | Invalid timestamp value | 400 |
| `TOO_MANY_KEYS` | Too many keys in batch request | 400 |

### Server Errors (5xx)

| Code | Description | HTTP Status |
|---|---|---|
| `INTERNAL` | Unexpected server error | 500 |
| `UPSERT_FAILED` | Failed to create or update a fact | 500 |
| `SCROLL_FAILED` | Failed to query facts from Qdrant | 500 |
| `DELETE_FAILED` | Failed to delete a fact | 500 |
| `READ_FAILED` | Failed to read a fact | 500 |
| `QUERY_FAILED` | Failed to query logs or review queue | 500 |
| `ENTRY_NOT_FOUND` | Requested entry not found | 404 |
| `APPEND_FAILED` | Failed to append log entry to SQLite | 500 |
| `EMBEDDING_API_ERROR` | Embedding API returned an error | 502 |
| `LLM_API_ERROR` | LLM API returned an error | 502 |
| `QDRANT_UNREACHABLE` | Qdrant is unreachable or returned an error | 502 |
| `INGEST_FAILED` | Failed to index content | 500 |
| `GIT_NOT_CONFIGURED` | Git operations requested but not configured | 400 |
| `RETRIEVAL_ERROR` | Failed to retrieve data from storage | 500 |
| `SERVICE_UNAVAILABLE` | Service temporarily unavailable | 503 |
| `SUPERSEDE_CREATE_FAILED` | Failed to create superseding fact | 500 |
| `SERVICE_UNAVAILABLE` | LLM not configured or unreachable | 503 |
| `GIT_NOT_CONFIGURED` | PR mode requires git provider config | 400 |
| `GIT_PROVIDER_ERROR` | Git provider returned an error | 502 |

### Rate Limiting

When rate-limited (requires `RAGAMUFFIN_RATE_LIMIT_ENABLED=true`), the server
responds with HTTP `429 Too Many Requests` and a `Retry-After` header indicating
seconds until the rate window resets.

---

## Architecture

```
┌────────────┐     ┌──────────────┐     ┌──────────┐
│   Agent    │────▶│  Ragamuffin  │────▶│  Qdrant  │
│ (curl/MCP) │◀────│  (Go binary) │◀────│ (vector) │
└────────────┘     │              │     └──────────┘
                   │  ┌─────────┐ │
                   │  │ SQLite  │ │
                   │  │ (logs)  │ │
                   │  └─────────┘ │
                   │  ┌─────────┐ │
                   │  │ Filesys │ │
                   │  │ (vault) │ │
                   │  └─────────┘ │
                   └──────────────┘
```

- **All endpoints return JSON** with a uniform error format: `{"error": true, "code": "ERROR_CODE", "message": "..."}`
- **MCP is a bolt-on** — the REST API is the primary interface. MCP mirrors REST tools.
- **No bridge needed** — agents talk HTTP directly. Ragamuffin manages indexing, chunking, embedding, and storage.
- **LLM is optional** — `/recall` and facts/logs work without it. `/ask` and semantic conflict audit require it.

---

## Design

- **Go.** Single static binary. No runtime, no pip, no `asyncio.create_task` at module level.
- **REST-first.** MCP is a bolt-on. The curl test is the test that matters.
- **Optional everything.** Only Qdrant and an embedding API are mandatory. LLM? Optional. Git? Optional. Auth? Trust the proxy.
- **Write-back built in.** Agents learn things. The vault should grow.
- **Structured data, too.** Facts (key-value) and logs (append-only) extend the vault beyond flat files.

---

## Status

Active development. v0.6 adds OIDC auth, per-vault fact isolation,
embeddings auto-detect, versioned supersede, snapshot restore detection,
Qdrant reconnection, webhook event emitters, session CRUD, and SSE events.

### Release History

| Version | Highlights |
|---|---|
| v0.6 | OIDC-native auth with discovery flow, per-vault fact isolation (dedicated Qdrant collections), configurable embedding dimensions with auto-detect probe, versioned supersede with integer version field, restore-from-snapshot detection, graceful Qdrant lifecycle with reconnection loop, webhook event emitters for fact lifecycle, fact-to-chunk bridge with source stale scan, sessions API (CRUD), SSE event stream, full documentation update |
| v0.5 | Fact lifecycle management (Pruner: stale, conflict, supersede scans), review queue API (`GET/POST /v1/review`, `GET /v1/review/stats`), fact update endpoints (`PUT/PATCH /v1/facts`), agent memory backend (per-agent Qdrant collections, session ingest, vault provisioning, cross-agent recall, OpenClaw + Hermes plugin adapters), SSE streaming, lint pass, test infrastructure |
| v0.4 | Multi-tenancy, authentication (API key + JWT), knowledge graph, CloudEvents, LLM timeout config, embedded web UI, built-in web dashboard |
| v0.3.4 | ldflags for `/version`, panic recovery middleware, LLM base URL normalization, CountFiles sync from Qdrant on restart |
| v0.3.3 | Tags fix for facts POST (`qdrant.NewValue` 2-value return), deployment fixes |
| v0.3.2 | (skipped — build failure) |
| v0.3.1 | UUID point IDs, Qdrant gRPC port (6334), healthcheck improvements |
| v0.3.0 | Facts endpoint, Logs endpoint, Snapshot endpoint, code-review fixes batch |

Named with affection by [Christopher Goulet](https://github.com/chezgoulet).

---

*"The monkey paw used curl. It was super effective!"*
