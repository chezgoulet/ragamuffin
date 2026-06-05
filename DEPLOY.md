# Deploying Ragamuffin

Ragamuffin runs as a two-container Docker Compose stack: the binary + a Qdrant
vector store. Other stacks (Hermes, OpenClaw) reach it over a shared Docker network.

## Quick Start

```bash
cd ~/infra/ragamuffin
cp .env.example .env
# Edit .env — add your embedding API key at minimum
docker compose up -d
```

Wait for initial indexing to complete (check `/health`), then:

```bash
curl -X POST http://localhost:8000/recall \
  -d '{"query":"what do we know about this project?"}'
```

## Configuration

Full variable reference: [SPEC.md](SPEC.md#configuration).
Quick-reference `.env.example` at the repo root.

| Env var | Required | Notes |
|---|---|---|
| `RAGAMUFFIN_VAULT_PATH` | conditional | Host path to vault (single-tenant). Mutually exclusive with `VAULTS`. |
| `RAGAMUFFIN_VAULTS` | conditional | `name:path,name:path,...` for multi-tenant. Mutually exclusive with `VAULT_PATH`. |
| `RAGAMUFFIN_QDRANT_URL` | yes | Qdrant gRPC endpoint |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | yes | OpenAI or compatible API key (omit to run without /recall) |
| `RAGAMUFFIN_LLM_*` | no | Enable `/ask` and semantic conflict detection |
| `RAGAMUFFIN_GIT_*` | no | Enable `/draft` PR mode |

## Multi-Tenant Vault Setup

Instead of a single vault, Ragamuffin can serve multiple isolated vaults:

```bash
RAGAMUFFIN_VAULTS=docs:/path/to/docs,code:/path/to/code
```

Each vault gets its own Qdrant collection for chunks and facts. Agents access
them via `/vault/{name}/` prefix on all content endpoints. See
[AGENTS_SKILL.md](AGENTS_SKILL.md#v04-endpoints) for the full vault-prefixed API.

Vaults are **physically isolated** at the Qdrant collection level — not metadata
filters — so a bug in one agent can't leak data into another agent's vault.

## Authentication & Authorization

Set `RAGAMUFFIN_AUTH_MODE` to one of four modes:

### `none` (default)
No auth. All endpoints are public. Safe for internal networks.

### `api_key`
Static API keys for read and write access:

```bash
RAGAMUFFIN_AUTH_MODE=api_key
RAGAMUFFIN_AUTH_READ_KEY=sk-read-abc123      # Read-only access
RAGAMUFFIN_AUTH_WRITE_KEY=sk-write-xyz789     # Read+write access
```

Clients include the key as a Bearer token:

```bash
curl -H "Authorization: Bearer sk-read-abc123" http://localhost:8000/recall -d '{"query":"..."}'
```

Read-key agents can search, browse, and audit. Write-key agents can also create
facts, write logs, use `/draft`, and call review endpoints.

### `jwt`
JWKS-based JWT verification:

```bash
RAGAMUFFIN_AUTH_MODE=jwt
RAGAMUFFIN_AUTH_JWT_ISSUER=https://auth.example.com
RAGAMUFFIN_AUTH_JWT_AUDIENCE=ragamuffin
RAGAMUFFIN_AUTH_JWT_JWKS_URL=https://auth.example.com/.well-known/jwks.json
```

Tokens are verified for signature, issuer, audience, and expiration. Claims
are decoded for RBAC (write access requires a write claim).

### `oidc`
OIDC discovery-based auth — automatically fetches JWKS from the OIDC issuer:

```bash
RAGAMUFFIN_AUTH_MODE=oidc
RAGAMUFFIN_AUTH_OIDC_ISSUER=https://accounts.example.com
RAGAMUFFIN_AUTH_OIDC_CLIENT_ID=ragamuffin
```

The JWKS endpoint is auto-discovered from the OIDC provider's
`.well-known/openid-configuration`. Token validation mirrors JWT mode.

### Auth Check Endpoint

```bash
curl -s http://localhost:8000/v1/auth/check
```

Returns the current auth mode, whether auth is enforced, and any decoding issues
with the provided token (if present). Useful for debugging auth configuration.

## Pruner (Background Fact Health)

The pruner is a background worker that maintains fact quality. It never deletes
facts — it marks them `needs_review`, `superseded`, or adjusts confidence.

Enable and configure:

```bash
RAGAMUFFIN_PRUNER_ENABLED=true
RAGAMUFFIN_PRUNER_STALE_INTERVAL=24h       # How often to scan for stale facts
RAGAMUFFIN_PRUNER_STALE_DAYS=90            # Days without update = stale
RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL=72h    # How often to scan for semantic conflicts
RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE=50  # Pairs to compare per scan
RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL=24h   # How often to check supersession chains
RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD=0.5  # Below this → needs_review
```

The pruner runs its scans on independent schedules. Each scan type can be
configured separately. Stale scan is the only one that runs without an LLM;
conflict scan requires an LLM provider to be configured.

Flagged facts appear in `GET /v1/review` and can be resolved via
`POST /v1/review` with confirm, supersede, reject, or reclassify actions.

## Snapshot & Restore

Download the full vault as a gzipped tarball:

```bash
# Download
curl -s -O http://localhost:8000/v1/snapshot

# Restore (on a fresh Ragamuffin instance)
tar xzf snapshot
mv snapshot/* /path/to/vault/
# Ragamuffin re-indexes on next watch cycle
```

Ragamuffin detects snapshot restore drift on startup. If the restored index
doesn't match the current index within `RAGAMUFFIN_RESTORE_MISMATCH_THRESHOLD`
(default 0.1), it logs a warning. Drift is reported via `/audit`.

## Webhook Events

Ragamuffin emits CloudEvents v1.0 structured JSON to a configured webhook URL
for fact lifecycle events:

```bash
RAGAMUFFIN_EVENT_WEBHOOK_URL=https://hooks.example.com/ragamuffin
```

Events are HTTP POST with `Content-Type: application/cloudevents+json`.
Fire-and-forget delivery — no retry, no persistence. The consumer is responsible
for durability.

### Lifecycle Events

| Event Type | When | Payload |
|---|---|---|
| `fact.created` | New fact upserted | `{"fact_key": "db/url", "status": "active"}` |
| `fact.updated` | Existing fact modified | `{"fact_key": "db/url", "previous_status": "active", "new_status": "superseded"}` |
| `fact.superseded` | Fact superseded by another | `{"fact_key": "db/url", "superseded_by": "db/url-v2"}` |
| `fact.rejected` | Review action: reject | `{"fact_key": "db/url", "reason": "outdated"}` |
| `fact.confirmed` | Review action: confirm | `{"fact_key": "db/url", "confidence": 0.95}` |
| `fact.needs_review` | Pruner flags a fact | `{"fact_key": "db/url", "reasons": ["stale", "low_confidence"]}` |

For real-time streaming without a webhook, connect to `/events` SSE endpoint:

```bash
curl -s -N http://localhost:8000/events
```

SSE events follow the same event types and are compatible with standard
EventSource clients.

## Access From Other Compose Stacks

Ragamuffin's containers live on a Docker network called `ragamuffin_ragamuffin`.
Other stacks join this network as external to reach the service.

### Hermes

In your hermes `docker-compose.yml`:

```yaml
services:
  hermes:
    networks:
      - hermes
      - ragamuffin_ragamuffin   # add this

networks:
  hermes:
    driver: bridge
  ragamuffin_ragamuffin:        # add this
    external: true
```

After `docker compose up -d`, the hermes container can reach ragamuffin at
`http://ragamuffin:8000`. No port publishing needed — Docker DNS resolves the
container name on the shared network.

### OpenClaw

Same pattern. In your OpenClaw `docker-compose.yml`, add `ragamuffin_ragamuffin`
as an external network and attach it to the gateway container. Agents inside
the gateway reach ragamuffin at `http://ragamuffin:8000`.

### Public Access (Optional)

If agents run outside the Docker host, expose ragamuffin through Traefik:

```yaml
services:
  ragamuffin:
    networks:
      - ragamuffin
      - proxy             # external Traefik network
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.ragamuffin.rule=Host(`ragamuffin.example.com`)"
      - "traefik.http.services.ragamuffin.loadbalancer.server.port=8000"

networks:
  ragamuffin:
    driver: bridge
  proxy:
    external: true
```

## Health Check

```bash
# Indexing progress
curl http://localhost:8000/health

# Stats
curl http://localhost:8000/stats

# Vault audit
curl -X POST http://localhost:8000/audit \
  -d '{"checks":["stale","gap","duplicate"]}'

# Auth status
curl http://localhost:8000/v1/auth/check

# Review queue (requires auth if configured)
curl http://localhost:8000/v1/review/stats
```

## Architecture

```
┌─ ragamuffin stack ──────────────────────┐
│  network: ragamuffin_ragamuffin          │
│                                          │
│  qdrant (internal) ←→ ragamuffin :8000  │
│                             │            │
│  ┌─ Background Workers ───┐│            │
│  │  Pruner (fact health)  ││            │
│  │  Watcher (file notify) ││            │
│  │  Reconnect (Qdrant)    ││            │
│  └────────────────────────┘│            │
└────────────────────┬───────┘            │
                     │                    │
         ┌───────────┼────────────┐       │
         │           │            │       │
    ┌────▼────┐ ┌────▼─────┐ ┌───▼──────┐ │
    │ hermes  │ │ openclaw │ │ traefik  │ │
    │ stack   │ │ stack    │ │(optional)│ │
    └─────────┘ └──────────┘ └──────────┘  │
                                           │
    Events (HTTP POST CloudEvents)         │
    ┌──────────────────────────────────────┘
    ▼
    webhook consumer
```

Qdrant is never published to the host or exposed to other stacks.
Only ragamuffin talks to it. External containers talk to ragamuffin,
not the database.

The background workers (pruner, watcher, Qdrant reconnection loop) run in the
same Ragamuffin process. No separate services needed.
