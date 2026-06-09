# Ragamuffin Integration Reference

How an OpenClaw agent interacts with Ragamuffin as its knowledge
backend. All examples use `curl` against `http://ragamuffin:8000`
— adapt for your agent harness (HTTP tool, Python, etc.).

## Table of Contents

1. [Health & Status](#1-health--status)
2. [Ingesting Content](#2-ingesting-content)
3. [Semantic Recall](#3-semantic-recall)
4. [Synthesis (Ask)](#4-synthesis-ask)
5. [Structured Facts](#5-structured-facts)
6. [Sessions & Procedural Memory](#6-sessions--procedural-memory)
7. [Events (SSE)](#7-events-sse)
8. [Vault Management](#8-vault-management)
9. [Common Patterns](#9-common-patterns)

---

## 1. Health & Status

Check if Ragamuffin is running and what version:

```bash
# Version info
curl -s http://ragamuffin:8000/version
# → {"version": "4406991a", "build_date": "...", "go_version": "go1.25.11"}

# Health check
curl -s http://ragamuffin:8000/health
# → {"status": "ok", "qdrant": "ok", "embedding": "ok", "llm": "ok", "indexing": true}
```

**What to do when it's down:** Wait and retry. Ragamuffin depends on
Qdrant (vector DB), an embedding service, and an LLM provider. If
Qdrant is down, facts and recall won't work. If LLM is down, `/ask`
synthesis won't work but recall and facts will.

---

## 2. Ingesting Content

Push documents, notes, or transcripts into Ragamuffin so they become
searchable via recall. Works with any text content.

### Single-tenant (default vault)

```bash
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "content": "Your document text here. Markdown supported.",
    "source": "notes/2026-06-09.md"
  }'
# → {"status": "ok", "vault": "default", "source": "notes/2026-06-09.md", "chunk_count": 3}
```

### Multi-tenant (named vault)

```bash
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "content": "Document text here.",
    "source": "notes/meeting-notes.md",
    "vault": "my-vault"
  }'
```

### With tags (for filtering)

```bash
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "content": "Meeting notes from sprint planning.",
    "source": "meetings/2026-06-09.md",
    "vault": "work",
    "tags": ["meeting", "sprint", "planning"]
  }'
```

**Key points:**
- Vault is auto-provisioned on first ingest if `AutoProvisionVaults=true`
- `source` should be a meaningful path — it's used for dedup and
  for the `vault.file.changed` event
- Tags help with filtering in recall queries
- Content is chunked, embedded, and stored in Qdrant automatically

---

## 3. Semantic Recall

Query Ragamuffin's vector index for chunks relevant to your question.
Returns results ranked by semantic similarity.

```bash
curl -s -X POST http://ragamuffin:8000/v1/recall \
  -H "Content-Type: application/json" \
  -d '{
    "query": "What was decided about the deployment strategy?",
    "top_k": 5
  }'
```

### With vault scope

```bash
curl -s -X POST http://ragamuffin:8000/vault/my-vault/recall \
  -H "Content-Type: application/json" \
  -d '{"query": "server configuration", "top_k": 3}'
```

### With tag filters

```bash
curl -s -X POST http://ragamuffin:8000/v1/recall \
  -H "Content-Type: application/json" \
  -d '{
    "query": "budget numbers",
    "top_k": 10,
    "filter": {"tags": ["financial", "q2"]}
  }'
```

### Batch recall (multiple queries at once)

```bash
curl -s -X POST http://ragamuffin:8000/vault/my-vault/v1/batch/recall \
  -H "Content-Type: application/json" \
  -d '{
    "queries": [
      {"query": "deployment strategy", "top_k": 3},
      {"query": "monitoring setup", "top_k": 3}
    ]
  }'
```

### Response format

```json
{
  "results": [
    {
      "score": 0.89,
      "content": "We decided to use Blue/Green deployment...",
      "source": "meetings/2026-06-09.md",
      "chunk_index": 2
    },
    ...
  ]
}
```

**Key points:**
- Recall returns raw chunks, not synthesized answers. Use `/ask`
  for synthesis.
- `top_k` defaults to 10. Higher values return more but slower.
- Tags filter on the `_tags` field in Qdrant.
- Batch recall is more efficient than multiple single queries.

---

## 4. Synthesis (Ask)

Get an LLM-generated answer grounded in your knowledge base.
Ragamuffin internally does recall + LLM synthesis.

```bash
curl -s -X POST http://ragamuffin:8000/v1/ask \
  -H "Content-Type: application/json" \
  -d '{
    "query": "What deployment strategy did we settle on?",
    "top_k": 10
  }'
# → {"answer": "We settled on Blue/Green deployment...", "sources": [...], "mode_used": "full"}
```

### With vault

```bash
curl -s -X POST http://ragamuffin:8000/vault/work/ask \
  -H "Content-Type: application/json" \
  -d '{"query": "What is the current sprint goal?"}'
```

### Usage guidelines for agents

Use `/ask` when:
- Someone asks a question that draws on stored knowledge
- You need a natural-language answer grounded in facts
- You want to synthesize across multiple documents

Use `/recall` when:
- You need raw source material to reason about
- You want to decide what to do with the information yourself
- Latency matters (recall is ~200ms, ask is ~1-2s)

---

## 5. Structured Facts

Facts are key-value pairs with optional metadata (source, tags,
confidence, vault scope). They persist across sessions and survive
restarts.

### Create a fact

```bash
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{
    "key": "user:christopher:timezone",
    "value": "America/New_York",
    "source": "conversation",
    "tags": ["preference", "timezone"]
  }'
# → {"key": "user:christopher:timezone", "status": "created"}
```

### Read a fact

```bash
curl -s http://ragamuffin:8000/v1/facts/user:christopher:timezone
# → {"key": "user:christopher:timezone", "value": "America/New_York", "source": "conversation"}
```

### List facts by prefix

```bash
curl -s "http://ragamuffin:8000/v1/facts?prefix=user:christopher&limit=20"
# → {"facts": [{...}, {...}], "total": 2}
```

### Supersede (update) a fact

```bash
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{
    "key": "user:christopher:timezone",
    "value": "America/Toronto",
    "source": "conversation",
    "supersede": true
  }'
```

### Vault-scoped facts

```bash
curl -s -X POST http://ragamuffin:8000/vault/my-vault/v1/facts \
  -H "Content-Type: application/json" \
  -d '{
    "key": "project:deadline",
    "value": "2026-06-30",
    "source": "planning"
  }'
```

### Fact graph (find related facts)

```bash
curl -s http://ragamuffin:8000/v1/facts/user:christopher:timezone/graph
```

### When to use facts vs. ingest

| Use facts for | Use ingest for |
|---|---|
| Preferences, decisions, settings | Documents, notes, transcripts |
| Key-value data that changes | Content you want to search |
| Things you reference by key | Things you reference by meaning |
| Agent state across restarts | Long-form knowledge |

---

## 6. Sessions & Procedural Memory

Ragamuffin tracks conversations as sessions — sequences of turns
with content and roles.

### List sessions

```bash
curl -s "http://ragamuffin:8000/v1/sessions?limit=10"
```

### Get a session

```bash
curl -s http://ragamuffin:8000/v1/sessions/{session-id}
```

### Finalize a session — extract procedural memory

When a session concludes, finalize it to extract reusable procedures:

```bash
curl -s -X POST "http://ragamuffin:8000/v1/sessions/{session-id}/finalize?extract_procedures=true" \
  -H "Content-Type: application/json"
```

This:
1. Marks the session as finalized
2. Extracts procedures (repeated action patterns with positive outcomes)
3. Deduplicates against existing procedures
4. Writes new procedures as facts (type `procedure-*`)

### How procedural memory works

Procedures are extracted from assistant turns that contain action
keywords (run, check, grep, write, edit, curl, etc.) and positive
outcome signals. They're stored as facts with deterministic keys
based on name+trigger hash. Dedup uses word overlap + bigram Dice
coefficient on procedure names (threshold: 0.85).

To enable: set `RAGAMUFFIN_PROCEDURAL_ENABLED=true` and pass
`?extract_procedures=true` on finalization.

---

## 7. Events (SSE)

Ragamuffin emits Server-Sent Events for state changes. Subscribe
to react to changes in real-time.

```bash
curl -N http://ragamuffin:8000/events
```

### Event types

| Event | Triggered by | Data payload |
|---|---|---|
| `connected` | Connection established | `{"id": <connection-id>}` |
| `vault.file.changed` | File ingested or modified | `{"path": "...", "action": "created"|"modified", "size": N}` |
| `vault.file.deleted` | File removed | `{"path": "..."}` |
| `fact.created` | New fact written | `{"key": "...", "value": "...", "source": "...", "vault": "..."}` |
| `fact.superseded` | Fact updated | `{"key": "...", "previous_value": "...", "new_value": "..."}` |
| `server.started` | Server boot | `{"host": "...", "commit": "...", "version": "..."}` |
| `pruner.complete` | Audit/stale check done | `{"vault": "...", "deleted": N}` |
| `fact.flagged` | Anomaly detected | `{"key": "...", "reason": "..."}` |

### SSE stream format (CloudEvents 1.0)

```
event: vault.file.changed
data: {"specversion":"1.0","id":"...","source":"ragamuffin","type":"vault.file.changed","time":"...","data":{"path":"...","action":"created","size":13}}

```

**Note on parsing:** When using a client library that wraps the SSE
`event:` and `data:` headers, the CloudEvent envelope is nested
inside the parsed `data` field, not at the top level of the event
object.

---

## 8. Vault Management

Vaults are isolated knowledge namespaces. Each vault has its own
indexer, Qdrant collection, and fact namespace.

### List vaults

```bash
curl -s http://ragamuffin:8000/vaults
# → {"vaults": [{"name": "default", "path": "/opt/vault/default", ...}, ...]}
```

Each vault object includes:
- `name`: Vault identifier
- `path`: Filesystem path
- `indexed_files`: Count of processed files
- `total_chunks`: Total chunks across all files
- `indexing`: Whether indexing is currently running
- `last_indexed`: Timestamp of last index

### Vault API pattern

All core endpoints are available vault-scoped:
- `/vault/{name}/recall` — recall within vault
- `/vault/{name}/ask` — synthesis within vault
- `/vault/{name}/draft` — draft email/reply using vault context
- `/vault/{name}/audit` — audit vault for stale/duplicate content
- `/vault/{name}/v1/facts` — vault-scoped facts
- `/vault/{name}/v1/ingest` — ingest into vault
- `/vault/{name}/v1/links` — cross-file links
- `/vault/{name}/v1/links/backlinks` — backlinks

---

## 9. Common Patterns

### Pattern A: Remember a user preference

```bash
# 1. Store the preference
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key": "user:alice:timezone", "value": "US/Eastern", "source": "conversation"}'

# 2. Retrieve it later
curl -s http://ragamuffin:8000/v1/facts/user:alice:timezone
```

### Pattern B: Research + synthesize

```bash
# 1. Ingest the source material
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{"content": "...", "source": "research/paper.md"}'

# 2. Query with synthesis
curl -s -X POST http://ragamuffin:8000/v1/ask \
  -H "Content-Type: application/json" \
  -d '{"query": "What does the paper say about X?"}'
```

### Pattern C: Agent state across restarts

```bash
# On shutdown — save state
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key": "agent:dev:last_task", "value": "completed issue #662", "source": "agent-shutdown"}'

# On startup — restore state
STATE=$(curl -s http://ragamuffin:8000/v1/facts/agent:dev:last_task \
  | python3 -c "import sys,json; print(json.load(sys.stdin).get('value',''))")
```

### Pattern D: React to file changes via SSE

```bash
# Subscribe and trigger actions on file changes
curl -N http://ragamuffin:8000/events | while read line; do
  if [[ "$line" == data:* ]]; then
    echo "Event received: $line"
    # Trigger workflow, re-index, notify, etc.
  fi
done
```

### Pattern E: Rate-limit aware usage

Ragamuffin rate limits per endpoint. Defaults:
- `/ask`: 10 req/min
- `/draft`: 30 req/min
- `/audit`: 5 req/min
- `/ingest`: 30 req/min

Unlimited endpoints: `/recall`, `/facts`, `/vaults`, `/health`,
`/version`, `/events`, `/sessions`.

If you get a 429, back off and retry. The token bucket refills at
RPM/60 tokens per second.

---

## Configuration (openclaw.json)

The Ragamuffin URL and optional API key are configured in the agent's
OpenClaw config. See `openclaw-config.json.example` for a full
example.

Key settings:

```json
{
  "url": "http://ragamuffin:8000",
  "api_key": "${RAGAMUFFIN_API_KEY}",
  "default_vault": "default",
  "timeout_ms": 10000
}
```

---

## Quick Reference Card

| Task | Endpoint | Method |
|---|---|---|
| Health | `/health` | GET |
| Version | `/version` | GET |
| Ingest content | `/v1/ingest` | POST |
| Recall | `/v1/recall` | POST |
| Synthesis | `/v1/ask` | POST |
| Draft | `/v1/draft` | POST |
| Audit | `/v1/audit` | POST |
| Create fact | `/v1/facts` | POST |
| Read fact | `/v1/facts/{key}` | GET |
| List facts | `/v1/facts?prefix=...` | GET |
| Fact graph | `/v1/facts/{key}/graph` | GET |
| List vaults | `/vaults` | GET |
| List sessions | `/v1/sessions` | GET |
| Get session | `/v1/sessions/{id}` | GET |
| Finalize session | `/v1/sessions/{id}/finalize` | POST |
| Events (SSE) | `/events` | GET |

All endpoints available as `/vault/{name}/...` for vault-scoped
operations.
