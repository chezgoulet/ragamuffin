# Ragamuffin Memory Provider API

A standalone HTTP contract that any agent harness can implement against to add
Ragamuffin-backed memory. The OpenClaw and Hermes plugin adapters are reference
implementations — this document is all you need to write an adapter for a third
harness.

> **Who this is for:** Harness authors who want to write a `memory-ragamuffin`
> adapter for a gateway, framework, or agent runtime that has a pluggable memory
> backend interface.
>
> Also: operators who want to use Ragamuffin as a **cross-harness bridge**
> alongside their existing memory slot (claudemem, Honcho, etc.) by giving
> agents direct API tools rather than swapping the slot itself.

## Quick Reference

```
POST   /v1/vaults                Create or confirm an agent vault
GET    /v1/vaults/:name/health   Check vault readiness
POST   /v1/ingest                Index content into a vault
POST   /v1/recall                Semantic search against a vault
```

## Vault Naming Conventions

Agent vaults use the namespace prefix `agent::` to distinguish them from
filesystem-based vaults:

| Agent | Vault name | Qdrant collection |
|---|---|---|
| dev (OpenClaw) | `agent::dev` | `agent::dev` |
| robot (Hermes) | `agent::robot` | `agent::robot` |
| scout | `agent::scout` | `agent::scout` |

The prefix is configurable in both adapters — it just needs to be consistent
within a deployment. All endpoints accept the full vault name as a path or
query parameter.

## Endpoints

### `POST /v1/vaults`

Create or confirm a vault exists. Idempotent — safe to call on every agent
startup. Creates the corresponding Qdrant collection if it doesn't exist.

**Request:**
```json
{
  "name": "agent::dev",
  "label": "Dev agent working memory"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Vault name. Use `agent::<id>` convention. |
| `label` | string | no | Human-readable label. |

**Response `201 Created`:**
```json
{
  "name": "agent::dev",
  "label": "Dev agent working memory",
  "created": true,
  "collection": "agent::dev"
}
```

**Response `200 OK`** (vault already existed):
```json
{
  "name": "agent::dev",
  "label": "Dev agent working memory",
  "created": false,
  "collection": "agent::dev"
}
```

**Errors:**
| Status | Code | When |
|---|---|---|
| 400 | `INVALID_INPUT` | Name is empty, too long (>256 chars), or contains invalid characters |
| 502 | `QDRANT_UNAVAILABLE` | Qdrant is not reachable |

### `GET /v1/vaults/:name/health`

Check that a vault exists and Qdrant is reachable. Used during agent startup
to gate readiness.

**Request:**
```
GET /v1/vaults/agent::dev/health
```

**Response `200 OK`:**
```json
{
  "name": "agent::dev",
  "exists": true,
  "collection": "agent::dev",
  "indexed": 142
}
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Vault name from the request |
| `exists` | boolean | Whether the vault/Qdrant collection exists |
| `collection` | string | Qdrant collection backing this vault |
| `indexed` | int | Number of points (documents) in the collection |

**Errors:**
| Status | Code | When |
|---|---|---|
| 502 | `QDRANT_UNAVAILABLE` | Qdrant is not reachable (vault may still exist) |

### `POST /v1/ingest`

Index content into an agent's vault. Called by the harness after each
completed turn to persist the exchange, and at session end to index a summary.

**Request:**
```json
{
  "vault": "agent::dev",
  "documents": [
    {
      "id": "turn-2026-05-17-001",
      "text": "User: what about Hermes integration?\nAssistant: Yes, Hermes has a MemoryProvider ABC...",
      "metadata": {
        "source": "session",
        "agent": "dev",
        "session_id": "sess_abc123"
      }
    }
  ]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `vault` | string | yes | Target vault name |
| `documents` | array | yes | List of documents to index |
| `documents[].id` | string | yes | Unique identifier (session ID + turn number, or UUID) |
| `documents[].text` | string | yes | Content text to embed and index |
| `documents[].metadata` | object | no | Arbitrary metadata attached to the Qdrant point |

**Response `200 OK`:**
```json
{
  "indexed": 1,
  "vault": "agent::dev"
}
```

**Response `207 Multi-Status`** (partial success):
```json
{
  "indexed": 2,
  "vault": "agent::dev",
  "errors": [
    {"id": "turn-001", "error": "duplicate_id"},
    {"id": "turn-002", "error": "text_too_long"}
  ]
}
```

**Errors:**
| Status | Code | When |
|---|---|---|
| 400 | `INVALID_INPUT` | Missing vault name, empty document list, or text too large (>64 KB) |
| 404 | `VAULT_NOT_FOUND` | Vault does not exist (call `POST /v1/vaults` first) |
| 502 | `QDRANT_UNAVAILABLE` | Qdrant is not reachable |

**Idempotency:** Re-ingesting the same document ID is safe — it upserts the
Qdrant point. The harness should generate stable IDs (e.g., `session_id + turn_number`).

### `POST /v1/recall`

Semantic search against a vault. The vault-targeted version of the root
`/recall` endpoint. Used by the harness during `prefetch()` and for
cross-agent recall.

**Request:**
```json
{
  "vault": "agent::dev",
  "query": "what did we decide about Qdrant isolation?",
  "limit": 5,
  "min_score": 0.5
}
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `vault` | string | yes | — | Vault to search |
| `query` | string | yes | — | Natural-language search query |
| `limit` | int | no | 10 | Max results (1–100) |
| `min_score` | float | no | 0.0 | Minimum similarity threshold (0.0–1.0) |

**Response `200 OK`:**
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
  "vault": "agent::dev",
  "query": "what did we decide about Qdrant isolation?"
}
```

**Errors:**
| Status | Code | When |
|---|---|---|
| 400 | `INVALID_INPUT` | Missing vault name or empty query |
| 404 | `VAULT_NOT_FOUND` | Vault does not exist |
| 502 | `QDRANT_UNAVAILABLE` | Qdrant is not reachable |

## Harness Lifecycle Mapping

Use this table to map your harness's memory provider interface to Ragamuffin's
HTTP API. Both the OpenClaw and Hermes adapters follow this exact mapping.

| Harness hook | Description | Ragamuffin call |
|---|---|---|
| `initialize(session_id)` | Agent starts, connect to backend | `POST /v1/vaults` with `name = prefix + agent_id` |
| `prefetch(query)` / `queue_prefetch()` | Recall context before each turn | `POST /v1/recall` against agent's own vault |
| `sync_turn(user_msg, asst_msg)` | Persist the completed exchange | `POST /v1/ingest` with turn as document |
| `on_session_end(messages)` | Session ended, persist summary | `POST /v1/ingest` with synthesized summary document |
| `get_tool_schemas()` | Expose memory tools to agent | Return static schemas for `ragamuffin_recall`, `ragamuffin_store`, `agent_recall` |
| `handle_tool_call('agent_recall', {vault, query})` | Cross-agent query | `POST /v1/recall?vault=agent::robot` |
| `shutdown()` | Clean shutdown | None needed — Qdrant persists data |

## Agent Identity

The harness adapter needs to know three things to derive the vault name:

1. **Agent identifier** — the agent's name, ID, or profile name (varies by harness)
2. **Vault prefix** — configurable, defaults to `agent::`
3. **Resulting vault name** = `{prefix}{agent_id}`, e.g. `agent::dev`

The harness should pass the agent identifier to `initialize()` or equivalent.
If the harness does not expose the agent identity, the adapter can use a
configurable static vault name.

## Hybrid Deployment: Agents as API Clients

If you're not ready to swap your harness memory slot, you can still use
Ragamuffin by giving agents two tools that call the API directly:

- `ragamuffin_store(vault, id, text, metadata)` → `POST /v1/ingest`
- `ragamuffin_recall(vault, query, limit)` → `POST /v1/recall`

The harness slot (claudemem, Honcho, etc.) continues to handle automatic
turn-by-turn memory. Agents use the Ragamuffin tools **selectively** — only for
decisions, conclusions, and facts worth sharing across harness boundaries.

### Tool definitions to add to your agent

**OpenClaw — custom tool:**
```json5
// Add to openclaw.json under the agent's tool definition
{
  name: "ragamuffin_recall",
  description: "Search an agent's knowledge vault. Use this to recall what another agent knows.",
  parameters: {
    type: "object",
    properties: {
      vault: { type: "string", description: "Agent vault name, e.g. agent::robot" },
      query: { type: "string", description: "Natural language query" },
      limit: { type: "integer", default: 5 },
    },
    required: ["vault", "query"],
  },
  handler: "http://ragamuffin:8000/v1/recall",
}
```

**Hermes — custom tool via function calling config:**
```yaml
# Extend agent config with Ragamuffin tools
tools:
  - name: ragamuffin_recall
    description: "Search an agent's knowledge vault"
    handler:
      url: "http://ragamuffin:8000/v1/recall"
      method: POST
      headers:
        Content-Type: application/json
```

### When agents should write vs. when the slot handles it

| Type | Handled by | Example |
|---|---|---|
| Turn transcript | Slot (auto) | "User asked about X. I responded Y." |
| Important decision | Agent (explicit) | "Decision: use Qdrant collection isolation, not metadata filters." |
| Cross-agent artifact | Agent (explicit) | "Deploy plan for v0.5 written to /ops/ragamuffin-v0.5-plan.md" |
| Session summary | Slot (auto) or Agent | Summaries at end of session |

Agents should store sparingly in hybrid mode — only what they'd want another
agent to find. The slot already captures everything else.

### Migration path from hybrid to full slot

1. **Phase 1 (hybrid):** Deploy Ragamuffin. Give agents tools. Validate that
   cross-harness recall works. No slot changes.
2. **Phase 2 (parallel):** Begin slot migration on non-critical agents. Run
   both backends — slot handles turn persistence, Ragamuffin handles
   cross-harness recall.
3. **Phase 3 (full):** Swap the slot to `memory-ragamuffin` on all agents.
   Ragamuffin now handles both turn persistence and cross-harness recall.
   Retire old slot backend.

---

## Auth Integration

If Ragamuffin has authentication enabled (`RAGAMUFFIN_AUTH_MODE=api_key` or
`=jwt`), the adapter must include an `Authorization: Bearer <key>` header on
all requests. The adapter should support configuration for:

```yaml
ragamuffin:
  endpoint: "http://ragamuffin:8000"
  vault_prefix: "agent::"
  auth_token: "sk-..."    # optional, for auth-enabled deployments
```

## Error Handling Guide

All endpoints return errors in a uniform format:

```json
{
  "error": true,
  "code": "ERROR_CODE",
  "message": "Human-readable description"
}
```

### What the adapter should do on each error

| Code | Adapter action |
|---|---|
| `INVALID_INPUT` | Log and abort the operation — this is a programming error in the adapter |
| `VAULT_NOT_FOUND` | Retry after calling `POST /v1/vaults` — the vault may not have been provisioned yet |
| `QDRANT_UNAVAILABLE` | Retry with exponential backoff (Qdrant may be starting up or restarting) |
| `RATE_LIMITED` | Wait for `Retry-After` header duration, then retry |
| `500` / `502` | Log and fail open — don't block the agent if memory is unavailable |
| Connection refused / timeout | Log and fail open — treat as transient infrastructure blip |

**Fail-open principle:** If Ragamuffin is unreachable, the agent should still
operate. It just won't have memory recall or persistence until the backend
comes back. The adapter should log the failure and return empty context /
silently skip persistence.

## OpenAPI Spec

An OpenAPI 3.0.3 specification is available at
[ragamuffin-memory-api.yaml](ragamuffin-memory-api.yaml) for code generation
or importing into API tooling.

## Reference Implementations

- **OpenClaw:** `plugins/memory-ragamuffin-openclaw/` (Node.js, ~250 lines)
- **Hermes:** `adapters/hermes-memory/` (Python, ~200 lines)

Both live in the [chezgoulet/ragamuffin](https://github.com/chezgoulet/ragamuffin) repo.

The adapter code is the best place to see the lifecycle mapping in action.
The API contract document is the reference — but the code is the test.
