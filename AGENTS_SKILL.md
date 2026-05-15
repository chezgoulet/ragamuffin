# Agent Skill: Ragamuffin Vault

Know an agent that needs to read, search, and write to a knowledge base?
Ragamuffin is a REST API that turns a directory of files into a queryable
vector store.

## Quickstart

```bash
# Discovery — check the vault is alive
curl -s http://ragamuffin:8000/health | jq .

# Quick info
curl -s http://ragamuffin:8000/stats | jq .
```

## Endpoints (Agent-Friendly Cheat Sheet)

### Semantic Search — `/recall`

Ask natural-language questions. Get back ranked chunks with source paths.

```bash
curl -s http://ragamuffin:8000/recall \
  -H "Content-Type: application/json" \
  -d '{"query": "What is our deployment process?", "top_k": 5}'
```

```json
{
  "results": [
    {"text": "Deployment uses GitHub Actions...", "source_file": "ops/deploy.md", "score": 0.89}
  ],
  "top_score": 0.89
}
```

`top_k` max is 100. `score_threshold` (0.0–1.0) filters by minimum similarity.
`source_filter` restricts results to files under a path prefix.

### Synthesized Answers — `/ask`

For questions that span multiple files. Requires LLM configured.

```bash
curl -s http://ragamuffin:8000/ask \
  -H "Content-Type: application/json" \
  -d '{"query": "Summarize our infrastructure", "mode": "auto"}'
```

Modes: `rag` (RAG only), `auto` (RAG, fallback to full if low confidence),
`full` (load entire source files for context).

### Write-Back — `/draft`

Agents contribute to the vault. Two modes:

**Direct** — writes immediately to the filesystem:
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "New Notes", "content": "# Notes\n...", "target_path": "ops/notes.md"}'
```

**PR** — opens a pull request (requires git config):
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "Update schema", "content": "...", "target_path": "ops/schema.md", "mode": "pr"}'
```

**Delete** — set `"delete": true` to remove a file:
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "Remove", "target_path": "ops/old.md", "delete": true}'
```

### Structured Facts — `/v1/facts`

Use these for small, structured pieces of knowledge that don't belong
in a markdown file (e.g. connection strings, URLs, configurations).

**Upsert:**
```bash
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key": "db/url", "value": "postgres://...", "tags": ["prod"]}'
```

**Retrieve by exact key:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?key=db/url"
```

**Search by text fragment (substring match on key):**
```bash
curl -s "http://ragamuffin:8000/v1/facts?prefix=db/"
```

**Filter by tag:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?tag=prod&prefix=db/"
```

**Delete:**
```bash
curl -s -X DELETE "http://ragamuffin:8000/v1/facts?key=db/url"
```

**Pagination:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?limit=20"
curl -s "http://ragamuffin:8000/v1/facts?limit=20&before=<next_token>"
```

### Structured Logging — `/v1/logs`

Append-only log stream. Every entry has an agent, type, body, optional
tags and timestamp. Great for agents recording what they did.

**Append:**
```bash
curl -s -X POST http://ragamuffin:8000/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"agent": "scout", "type": "scan", "body": "Found 3 npm vulns", "tags": ["security"]}'
```

**Query with filters:**
```bash
curl -s "http://ragamuffin:8000/v1/logs?agent=scout&type=scan&tag=security&limit=10"
```

### Snapshot — `/v1/snapshot`

Download the entire vault as a gzipped tarball:
```bash
curl -s -O http://ragamuffin:8000/v1/snapshot
```

### Audit — `/audit`

Check vault health:
```bash
curl -s http://ragamuffin:8000/audit \
  -H "Content-Type: application/json" \
  -d '{}'
```

Returns stale files, semantic conflicts (requires LLM), gaps, and duplicates.

## Agent Workflow Patterns

### Before answering a question

1. Check `/stats` to see what's indexed
2. Use `/recall` with the question as query to find relevant chunks
3. For complex questions, use `/ask` to synthesize

### After learning something new

1. For prose (markdown docs) → use `/draft` with `mode: "direct"`
2. For structured data (configs, URLs) → use POST `/v1/facts`
3. For a record of what you did → use POST `/v1/logs`

### Periodic maintenance

1. Call `/audit` to check for stale files
2. Call `/v1/snapshot` to back up the vault
3. For git-backed vaults, use `/draft` with `mode: "pr"` for human review

## Configuration for Agents

The following env vars are available to agents composing Ragamuffin deployment:

```yaml
RAGAMUFFIN_VAULT_PATH=/opt/vault       # Where your knowledge lives
RAGAMUFFIN_QDRANT_URL=http://qdrant:6333    # Vector DB
RAGAMUFFIN_EMBEDDING_API_KEY=sk-...         # For embedding text into vectors
RAGAMUFFIN_LLM_API_KEY=sk-...               # For /ask and audit (optional)
RAGAMUFFIN_LLM_MODEL=gpt-4o                 # Which model to use
RAGAMUFFIN_GIT_TOKEN=ghp_...                # For PR mode (optional)
```

## Error Handling

All errors return:

```json
{"error": true, "code": "ERROR_CODE", "message": "Human-readable description"}
```

Common codes: `INVALID_INPUT`, `RATE_LIMITED` (with `Retry-After` header),
`NOT_FOUND`, `LLM_NOT_CONFIGURED`, `GIT_NOT_CONFIGURED`.

## Rate Limit Guidance

If your agent gets a `429` response, check the `Retry-After` header and
wait before retrying. Adjust rate limit env vars per-endpoint if needed
(see README.md for defaults and env vars).

## Discovery

```bash
# What version is running?
curl -s http://ragamuffin:8000/version

# Is the server healthy and Qdrant reachable?
curl -s http://ragamuffin:8000/health

# What endpoints are available?
# Ragamuffin doesn't have a discovery endpoint — this document IS the discovery.
# The full API surface is: /recall /ask /draft /audit /v1/facts /v1/logs
# /v1/snapshot /health /stats /version /metrics /mcp
```
