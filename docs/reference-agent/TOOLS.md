# TOOLS.md — Tool Access for Ragamuffin-Powered Agent

## MCP integration (recommended)

The most capable way to connect is through Ragamuffin's **MCP endpoint**
at `http://ragamuffin:8000/mcp`. The `memory-ragamuffin-openclaw` plugin
dynamically discovers all 33 tools and registers them automatically.

If your harness does not support MCP, the REST API is also available via
`exec` + `curl` (see below).

## Using exec for Ragamuffin calls

The alternative integration path is `exec` + `curl`. All Ragamuffin API
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
