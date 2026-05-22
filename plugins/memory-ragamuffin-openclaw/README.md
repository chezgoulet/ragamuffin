# memory-ragamuffin-openclaw

OpenClaw memory plugin backed by Ragamuffin — per-agent Qdrant-isolated
semantic search via vault recall. All embedding happens server-side in
Ragamuffin; no local embedding model is required.

## Install

This is an in-house plugin, not on npm. Install by linking the plugin
directory into your OpenClaw plugins load path:

```bash
# From the ragamuffin repo root
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

| Tool | Description |
|---|---|
| `memory_recall` | Semantic search across vault memories |
| `memory_store` | Save a fact (key-value) to vault |
| `memory_forget` | Delete a fact by key |

## Hooks

| Hook | Condition | Behavior |
|---|---|---|
| `before_prompt_build` | `autoRecall: true` | Injects relevant memories as XML context |
| `agent_end` | `autoCapture: true` | Stores important user statements as facts |

## Fail-Open

If Ragamuffin is unreachable, the plugin logs a warning and returns:
- Tool calls return a descriptive error message.
- Hooks silently skip injection — the agent continues without memory.

## Development

```bash
cd plugins/memory-ragamuffin-openclaw

# Run tests (no npm install needed — uses Node's native test runner)
node --test 'tests/*.test.js'
```
