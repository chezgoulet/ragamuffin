# memory-ragamuffin-openclaw

OpenClaw memory plugin backed by Ragamuffin via **Model Context Protocol (MCP)**.
Discovers all server tools dynamically — no hardcoded tool definitions needed.
All embedding happens server-side; no local embedding model required.

The plugin connects to Ragamuffin's `/mcp` endpoint using JSON-RPC 2.0,
dynamically registers every tool the server exposes, and keeps the MCP
connection for lifecycle hooks (auto-recall, auto-capture).

## Install

```bash
cp -r plugins/memory-ragamuffin-openclaw /path/to/openclaw/plugins/
```

## Configure

Set the active memory slot and plugin config in `openclaw.json`:

```jsonc
{
  "plugins": {
    "slots": {
      "memory": "memory-ragamuffin-openclaw"
    },
    "entries": {
      "memory-ragamuffin-openclaw": {
        "path": "/path/to/plugins/memory-ragamuffin-openclaw",
        "config": {
          "endpoint": "http://ragamuffin:8000",
          "authToken": "${RAGAMUFFIN_AUTH_TOKEN}",
          "vaultPrefix": "agent::",
          "autoRecall": true,
          "autoCapture": false,
          "recallLimit": 5,
          "recallThreshold": 0.3
        }
      }
    }
  }
}
```

All config values can also be set via environment variables:
- `RAGAMUFFIN_ENDPOINT` (default: `http://localhost:8000`)
- `RAGAMUFFIN_AUTH_TOKEN` (default: empty)
- `RAGAMUFFIN_VAULT_PREFIX` (default: `agent::`)

Config values take precedence over env vars.

## Tools

Dynamically discovered from the MCP server on startup. Typically includes:
- `memory.recall`, `memory.ask`, `memory.store`, `memory.hybrid_search`
- `memory.fact_get`, `memory.fact_put`, `memory.fact_list`, `memory.fact_delete`
- `memory.fact_graph`, `memory.fact_history`, `memory.fact_provenance`
- `memory.review`, `memory.verify`, `memory.context_bundle`, `memory.dialectic`
- `memory.peer_list`, `memory.briefing`, `memory.changes`
- `memory.contradictions`, `memory.links`, `memory.draft`, `memory.audit`
- `memory.graph_entity`, `memory.graph_edges`, `memory.graph_communities`
- `memory.stats`, `memory.status`
- `memory.session_create`, `memory.session_get`, `memory.session_list`
- `memory.turn_append`, `memory.get_chunk`, `memory.facts`

Run `ragamuffin tools` via the OpenClaw CLI to see the full live list (typically ~33 tools).

## Hooks

| Hook | Condition | Behavior |
|------|-----------|----------|
| `before_prompt_build` | `autoRecall: true` | Injects relevant memories as XML context via MCP `memory.recall` |
| `agent_end` | `autoCapture: true` | Stores important user statements via MCP `memory.fact_put` |

## Fail-Open

If Ragamuffin is unreachable, the plugin logs a warning and:
- Tool registration is skipped (agent gets no Ragamuffin tools, runs normally)
- Auto-recall/auto-capture silently skip their hooks
- The agent continues without memory

## Development

```bash
cd plugins/memory-ragamuffin-openclaw
node --test 'tests/*.test.js'
```
