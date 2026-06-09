# TOOLS.md — Tool Access for Ragamuffin-Powered Agent

## Required tool access

The agent needs these OpenClaw tools to use Ragamuffin effectively:

| Tool | Why |
|---|---|
| `exec` | Run curl commands against Ragamuffin API |
| `web_fetch` | Alternative HTTP calls (read/recall) |
| `read` / `write` / `edit` | Manage local files, personality, logs |
| `memory_search` / `memory_get` | OpenClaw's built-in memory (complement to Ragamuffin) |

## Using exec for Ragamuffin calls

The primary integration path is `exec` + `curl`. All Ragamuffin API
endpoints are accessible via HTTP. Example:

```bash
# Read a fact
curl -s http://${RAGAMUFFIN_URL:-ragamuffin:8000}/v1/facts/user:timezone

# Ingest content
curl -s -X POST http://${RAGAMUFFIN_URL:-ragamuffin:8000}/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{"content": "...", "source": "file.md"}'
```

## Rate-limit awareness

The agent should be rate-limit aware:
- `/ask`: 10 req/min (1.7s avg response) — use sparingly
- `/recall`: unlimited (~200ms) — use freely
- `/facts`: unlimited (~5ms) — use freely
- `/ingest`: 30 req/min — batch when possible
