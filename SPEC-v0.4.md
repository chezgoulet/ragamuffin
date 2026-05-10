# Ragamuffin v0.4 — Multi-Agent & Scale

Extends [SPEC-v0.3.md](SPEC-v0.3.md). All v0.1–v0.3 endpoints and behaviors
remain unchanged unless explicitly noted.

## Overview

v0.4 makes Ragamuffin shared infrastructure. One instance, many vaults, many
teams. Three pillars:

1. **Multi-tenancy** — multiple vaults on one Ragamuffin instance
2. **Authentication** — API keys and optional JWT for teams that want
   defense in depth
3. **Graph exploration** — agents navigate knowledge structure, not just
   search it

Plus a read-only Web UI for humans who need to peek at what the agents see.

---

## New Feature: Multi-Tenancy

### The Model

One Ragamuffin instance serves multiple vaults. Each vault is a named
collection with its own Qdrant collection, embedding config, and indexer.

### Configuration

```env
# Single vault (v0.1–v0.3, still supported)
RAGAMUFFIN_VAULT_PATH=/opt/vault

# Multiple vaults (v0.4)
RAGAMUFFIN_VAULTS=docs:/opt/vault-docs,code:/opt/vault-code,finance:/opt/vault-finance
```

When `RAGAMUFFIN_VAULTS` is set, single-vault `RAGAMUFFIN_VAULT_PATH` is
ignored. Multi-vault and single-vault are mutually exclusive — a Ragamuffin
instance is either single-tenant or multi-tenant, not both.

Each vault entry is `name:path`. Vault names must be lowercase alphanumeric
with hyphens, max 32 characters.

### API Changes

All existing endpoints move under a vault prefix when multi-tenancy is active:

| v0.3 | v0.4 (multi-tenant) |
|---|---|
| `/recall` | `/vault/{name}/recall` |
| `/ask` | `/vault/{name}/ask` |
| `/draft` | `/vault/{name}/draft` |
| `/audit` | `/vault/{name}/audit` |

Instance-wide endpoints stay at the root:

| Endpoint | Scope |
|---|---|
| `/health` | Instance health (all vaults) |
| `/stats` | Instance stats (all vaults aggregated) |
| `/version` | Build info |
| `/metrics` | Instance metrics |
| `/vaults` | List configured vaults |

### New Endpoint: `/vaults` — GET

Lists all configured vaults with their status.

```json
{
  "vaults": [
    {
      "name": "docs",
      "path": "/opt/vault-docs",
      "indexed_files": 247,
      "total_chunks": 1893,
      "last_indexed": "2026-05-10T01:30:00Z",
      "indexing": false
    },
    {
      "name": "code",
      "path": "/opt/vault-code",
      "indexed_files": 89,
      "total_chunks": 456,
      "last_indexed": "2026-05-10T01:28:00Z",
      "indexing": false
    }
  ]
}
```

No authentication required. This is a discovery endpoint — agents use it
to learn which vaults are available.

### Per-Vault Configuration

Each vault can override instance-level defaults:

```env
RAGAMUFFIN_VAULTS=docs:/opt/docs,code:/opt/code
RAGAMUFFIN_VAULT_DOCS_CHUNK_STRATEGY=heading
RAGAMUFFIN_VAULT_CODE_CHUNK_STRATEGY=fixed
RAGAMUFFIN_VAULT_CODE_CHUNK_FIXED_SIZE=500
```

Per-vault env vars follow the pattern `RAGAMUFFIN_VAULT_{NAME}_{SETTING}` where
`{NAME}` is the uppercased vault name. Overridable settings:

- `CHUNK_STRATEGY`
- `CHUNK_MAX_TOKENS`
- `CHUNK_FIXED_SIZE`
- `CHUNK_FIXED_OVERLAP`
- `EMBEDDING_MODEL`
- `EMBEDDING_DIMS`
- `AUDIT_ENTITY_EXTRACTION`
- `AUDIT_ENTITY_LLM`

LLM and embedding API keys are instance-wide (one key for all vaults).

### Backward Compatibility

Single-vault mode (v0.1–v0.3 behavior) is unchanged. Set `RAGAMUFFIN_VAULT_PATH`
and all endpoints work at the root. Multi-tenancy is opt-in — set
`RAGAMUFFIN_VAULTS` and the vault-prefixed routes activate.

---

## New Feature: Authentication

v0.1–v0.3 trust the reverse proxy. v0.4 adds optional API key and JWT
authentication for teams that want defense in depth.

### Modes

| Mode | Behavior |
|---|---|
| `none` (default) | v0.1–v0.3 behavior. No auth. Trust the proxy. |
| `api_key` | Static API keys. One key for read, one for write. |
| `jwt` | JWT validation against a configured issuer. Claims determine access. |

### API Key Mode

```env
RAGAMUFFIN_AUTH_MODE=api_key
RAGAMUFFIN_AUTH_READ_KEY=rm-read-abc123
RAGAMUFFIN_AUTH_WRITE_KEY=rm-write-xyz789
```

Requests must include `Authorization: Bearer <key>`.

- **Read key** grants access to `/recall`, `/ask`, `/health`, `/stats`,
  `/metrics`, `/audit` (read-only checks).
- **Write key** grants read access plus `/draft` and `/audit` with
  `semantic_conflict` and `entity_conflict` checks.

Write key also grants read access — having both keys is not a requirement
for agents that need to both read and write.

Invalid or missing keys return:

```json
{
  "error": true,
  "code": "UNAUTHORIZED",
  "message": "Invalid or missing API key"
}
```

Status code: `401 Unauthorized`.

### JWT Mode

```env
RAGAMUFFIN_AUTH_MODE=jwt
RAGAMUFFIN_AUTH_JWT_ISSUER=https://auth.example.com
RAGAMUFFIN_AUTH_JWT_AUDIENCE=ragamuffin
RAGAMUFFIN_AUTH_JWT_JWKS_URL=https://auth.example.com/.well-known/jwks.json
```

Tokens must include a `ragamuffin` claim:

```json
{
  "sub": "agent-hermes",
  "iss": "https://auth.example.com",
  "aud": "ragamuffin",
  "ragamuffin": {
    "access": "read_write",
    "vaults": ["docs", "code"]
  }
}
```

- `access`: `read_only` or `read_write`
- `vaults`: list of vault names this agent can access. Omitted = all vaults.

The JWT is validated on every request. JWKS is cached for 5 minutes.

### Per-Vault Keys (API Key Mode)

When multi-tenancy is active, API keys can be scoped to specific vaults:

```env
RAGAMUFFIN_AUTH_READ_KEY_DOCS=rm-read-docs-abc
RAGAMUFFIN_AUTH_WRITE_KEY_CODE=rm-write-code-xyz
```

An agent with the docs read key can access `/vault/docs/*` but not
`/vault/code/*` or `/vault/finance/*`. Unscoped keys (without a vault suffix)
grant access to all vaults.

### Backward Compatibility

When `RAGAMUFFIN_AUTH_MODE` is `none` (default), all endpoints are open.
Existing v0.1–v0.3 deployments need no changes.

---

## New Feature: Knowledge Graph

Agents don't just search — they navigate. The `/graph` endpoint returns entity
relationships extracted from the vault.

### `/graph` — GET

Returns a graph of entity co-occurrences and file cross-references.

**Query parameters:**

| Parameter | Type | Default | Notes |
|---|---|---|---|
| `vault` | string | — | Vault name (required in multi-tenant mode) |
| `entity` | string | — | Focus on a specific entity. Omit for full graph. |
| `depth` | integer | `1` | How many hops from the entity (1–3) |
| `limit` | integer | `50` | Max nodes to return |

**Response:**

```json
{
  "nodes": [
    {"id": "file:contractors/rates.md", "type": "file", "label": "Contractor Rates"},
    {"id": "entity:Qdrant", "type": "entity", "label": "Qdrant", "entity_type": "named"},
    {"id": "entity:$500/month", "type": "entity", "label": "$500/month", "entity_type": "currency"},
    {"id": "file:infra/costs.md", "type": "file", "label": "Infrastructure Costs"}
  ],
  "edges": [
    {"source": "file:contractors/rates.md", "target": "entity:$500/month", "relationship": "contains"},
    {"source": "file:infra/costs.md", "target": "entity:Qdrant", "relationship": "references"},
    {"source": "file:infra/costs.md", "target": "file:contractors/rates.md", "relationship": "links_to"}
  ]
}
```

Edge types:
- `contains` — the file chunk contains this entity
- `references` — the file references this entity
- `links_to` — the file has a wikilink or markdown link to the other file

### Graph Computation

The graph is computed during indexing from:
1. **Entities** — already extracted for audit (v0.3). Each entity gets a node.
2. **Wikilinks** — `[[path/to/file]]` in markdown becomes a `links_to` edge.
3. **Markdown links** — `[text](path/to/file.md)` becomes a `links_to` edge.

No additional LLM calls. No additional indexing pass. The data is already
there from entity extraction — the graph is just a different view of it.

### Use Cases

| Agent needs to... | Uses |
|---|---|
| "What other files reference this budget?" | `GET /graph?entity=entity:$500/month&depth=1` |
| "Show me everything connected to Qdrant" | `GET /graph?entity=entity:Qdrant&depth=2` |
| "Give me the full knowledge map" | `GET /graph` (limited to `limit` nodes) |

---

## New Feature: Web UI (Read-Only)

A minimal web interface served by the Ragamuffin binary at `/`.

### What It Does

- **Search** — type a query, see ranked results with source files
- **Browse** — navigate the vault's directory structure
- **Audit dashboard** — see the latest audit results at a glance
- **Graph explorer** — clickable entity graph (from `/graph` endpoint)

### What It Doesn't Do

- Edit files (API-only)
- Create PRs (API-only)
- Manage users
- Configure settings
- Upload files
- Chat interface

### Implementation

The Web UI is a set of static HTML/CSS/JS files embedded in the Ragamuffin
binary via `embed.FS`. It calls the same REST API that agents use — no
special backend routes. The UI is a consumer of the API, not an extension
of it.

Served at `/` with a catch-all that falls through to the API routes.
Root `/` returns `index.html`. API routes (`/health`, `/recall`, etc.)
are checked first and take priority.

No build step for the UI in the Ragamuffin build pipeline — the UI is
developed separately and the built artifacts are committed to the repo.

---

## New Endpoint: `/vault/{name}/reindex` — POST

Triggers a full re-index of a specific vault. Returns immediately with
`202 Accepted`. Indexing progress is tracked via `/health`.

```json
{
  "status": "accepted",
  "vault": "docs",
  "message": "Re-index started. Monitor progress via /health."
}
```

No request body. No parameters. This is an admin operation — it requires
write-key authentication if auth is enabled.

---

## New Environment Variables (v0.4)

| Variable | Required | Default | Notes |
|---|---|---|---|
| `RAGAMUFFIN_VAULTS` | no | — | `name:path,name:path` for multi-tenancy |
| `RAGAMUFFIN_AUTH_MODE` | no | `none` | `none`, `api_key`, or `jwt` |
| `RAGAMUFFIN_AUTH_READ_KEY` | conditional | — | Read API key (api_key mode) |
| `RAGAMUFFIN_AUTH_WRITE_KEY` | conditional | — | Write API key (api_key mode) |
| `RAGAMUFFIN_AUTH_JWT_ISSUER` | conditional | — | JWT issuer URL (jwt mode) |
| `RAGAMUFFIN_AUTH_JWT_AUDIENCE` | conditional | — | JWT audience (jwt mode) |
| `RAGAMUFFIN_AUTH_JWT_JWKS_URL` | conditional | — | JWKS endpoint URL (jwt mode) |

---

## Docker Compose Changes (v0.4)

No new services. The `ragamuffin` service gains new env vars for auth:

```yaml
services:
  ragamuffin:
    environment:
      RAGAMUFFIN_VAULTS: ${RAGAMUFFIN_VAULTS:-}
      RAGAMUFFIN_AUTH_MODE: ${RAGAMUFFIN_AUTH_MODE:-none}
      RAGAMUFFIN_AUTH_READ_KEY: ${RAGAMUFFIN_AUTH_READ_KEY:-}
      RAGAMUFFIN_AUTH_WRITE_KEY: ${RAGAMUFFIN_AUTH_WRITE_KEY:-}
      # ... JWT vars if needed ...
```

---

## Testing Requirements (v0.4)

- All v0.1–v0.3 tests must still pass.
- New: multi-tenancy routing tests (verify vault prefix routing).
- New: API key auth tests (valid, invalid, missing, scoped).
- New: JWT auth tests (valid token, expired, wrong audience, wrong claims).
- New: `/graph` endpoint tests (entity graph, file links, depth parameter).
- New: Web UI smoke tests (root returns HTML, API routes still work).
- New: `/vaults` list endpoint test.
- Coverage target: ≥ 80% on `internal/auth/`, `internal/tenant/`.

---

## Breaking Changes

**Single-vault users:** None. `RAGAMUFFIN_VAULT_PATH` still works. All
endpoints still work at the root. Auth defaults to `none`. v0.4 is a
drop-in replacement.

**Multi-vault adopters:** Endpoint paths change to `/vault/{name}/*`.
Agents must be updated with the vault prefix. The `/vaults` discovery
endpoint helps with this migration.
