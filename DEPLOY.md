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

| Env var | Required | Notes |
|---|---|---|
| `RAGAMUFFIN_VAULT_PATH` | yes | Host path to your vault repo |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | yes | OpenAI or compatible API key |
| `RAGAMUFFIN_LLM_*` | no | Enable `/ask` and semantic conflict detection |
| `RAGAMUFFIN_GIT_*` | no | Enable `/draft` PR mode |

Full reference: [SPEC.md](SPEC.md)

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
```

## Architecture

```
┌─ ragamuffin stack ─────────────────────┐
│  network: ragamuffin_ragamuffin         │
│                                         │
│  qdrant (internal) ←→ ragamuffin :8000 │
└──────────────────────┬─────────────────┘
                       │
         ┌─────────────┼─────────────┐
         │             │             │
    ┌────▼────┐  ┌─────▼──────┐  ┌──▼───────┐
    │ hermes  │  │  openclaw  │  │ traefik  │
    │ stack   │  │  stack     │  │(optional)│
    └─────────┘  └────────────┘  └──────────┘
```

Qdrant is never published to the host or exposed to other stacks.
Only ragamuffin talks to it. External containers talk to ragamuffin,
not the database.
