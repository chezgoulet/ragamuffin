# Ragamuffin Hermes Memory Adapter

Per-agent Qdrant-isolated memory with cross-agent recall, semantic search, and
fact-graph reasoning — exposed as a Hermes **MemoryProvider** plugin.

---

## Installation

```bash
hermes memory install ragamuffin
# or, from a local checkout:
hermes memory install /path/to/adapters/hermes-memory/
```

The adapter is auto-discovered via `plugin.yaml` and `register()`.

---

## Configuration

Ragamuffin uses a three-tier config resolution (later overrides earlier):

| Priority | Source | Example |
|---|---|---|
| 1 (lowest) | Defaults | `endpoint: http://ragamuffin:8000` |
| 2 | `$HERMES_HOME/ragamuffin.json` | Profile-scoped config file |
| 3 (highest) | Environment variables | `RAGAMUFFIN_ENDPOINT` |

### Option A: Environment variables (recommended per-profile)

Set these in each Hermes profile's `.env` file:

| Variable | Default | Description |
|---|---|---|
| `RAGAMUFFIN_ENDPOINT` | `http://ragamuffin:8000` | Ragamuffin server URL |
| `RAGAMUFFIN_AUTH_TOKEN` | *(empty)* | API key or JWT |
| `RAGAMUFFIN_VAULT_PREFIX` | `agent::` | Prefix for agent vault names |
| `RAGAMUFFIN_RECALL_MODE` | `hybrid` | `hybrid`, `context`, or `tools` |
| `RAGAMUFFIN_SAVE_MESSAGES` | `true` | Persist messages to memory |
| `RAGAMUFFIN_INJECTION_FREQ` | `every_turn` | `every_turn` or `first_turn` |
| `RAGAMUFFIN_CONTEXT_CADENCE` | `3` | Refresh base context every N turns (`0`=disable) |
| `RAGAMUFFIN_DIALECTIC_CADENCE` | `5` | Refresh dialectic every N turns (`0`=disable) |
| `RAGAMUFFIN_DIALECTIC_DEPTH` | `1` | Multi-pass reasoning levels: `1`, `2`, or `3` |
| `RAGAMUFFIN_EMPTY_STREAK_BACKOFF` | `true` | Skip dialectic after consecutive empty returns |
| `RAGAMUFFIN_CROSS_AGENT_VAULTS` | *(empty)* | Comma-separated vault names for cross-agent recall |
| `RAGAMUFFIN_FACT_GRAPH_ENABLED` | `true` | Enable fact-graph reasoning |
| `RAGAMUFFIN_CONFIG` | *(auto)* | Explicit path to `ragamuffin.json` |

### Option B: Config file (`$HERMES_HOME/ragamuffin.json`)

```json
{
  "endpoint": "http://ragamuffin:8000",
  "auth_token": "",
  "vault_prefix": "agent::",
  "recall_mode": "hybrid",
  "save_messages": true,
  "injection_freq": "every_turn",
  "context_cadence": 3,
  "dialectic_cadence": 5,
  "dialectic_depth": 1
}
```

Use `hermes ragamuffin setup` to create this file interactively:

```bash
hermes ragamuffin setup \
  --endpoint http://ragamuffin:8000 \
  --vault-prefix agent::
```

---

## Verification

```bash
# Quick health check
hermes ragamuffin status

# Full diagnostics
hermes ragamuffin doctor

# Check that the provider loaded
hermes memory list
# Expected: ragamuffin (0.9.3) - Per-agent Qdrant-isolated memory...
```

---

## Tools (11 schemas)

Once activated, the provider exposes these tool schemas to Hermes agents:

| Tool | Description |
|---|---|
| `ragamuffin_save` | Persist a piece of information to the agent's vault |
| `ragamuffin_recall` | Semantic search over the agent's memory |
| `ragamuffin_cross_recall` | Search another agent's vault by identity |
| `ragamuffin_context` | Retrieve full session context snapshot |
| `ragamuffin_review_page` | Paginated review of stored memories |
| `ragamuffin_conclude` | Write a persistent conclusion fact |
| `ragamuffin_profile` | Read or update the agent's peer card |
| `ragamuffin_forget` | Delete a specific memory by ID |
| `ragamuffin_remember` | Elicit a distilled memory from the agent |
| `ragamuffin_amend` | Correct a previously stored memory |
| `ragamuffin_review_resolve` | Cross-reference fact-graph entries |

---

## Built-in Agent Behavior

### Auto-Injection

When the adapter is active, Hermes automatically injects context-relevant
memory at every turn (configurable via `RAGAMUFFIN_INJECTION_FREQ`):

- **Context injection** — compacted recent history and relevant results from
  `recall()`, refreshed every `context_cadence` turns.
- **Dialectic injection** — synthesized reasoning from depth-graded queries,
  refreshed every `dialectic_cadence` turns.

### Persistence

Messages are saved to Ragamuffin automatically on `on_session_end()`
and periodically during `sync_turn()`. This includes both user messages
and agent responses.

### Cross-Agent Recall

Set `RAGAMUFFIN_CROSS_AGENT_VAULTS` to a comma-separated list of vault names
(e.g., `agent::robot,agent::scout`) to enable the agent to search other
agents' memory vaults via `ragamuffin_cross_recall`.

---

## Known Limitations

### Sessions API 503

The Ragamuffin `/sessions` API endpoint may return **503 Service Unavailable**
under load or when the Qdrant sessions collection is not yet initialized.
This is a recognized limitation — the adapter handles this gracefully by
falling back to available tool-based operations. The memory provider remains
fully functional for save, recall, and reasoning operations.

**Status:** Acknowledged, not fixed in v0.9.3. See issue #774.

### Config File Path

`$HERMES_HOME/ragamuffin.json` must be readable at process startup. If the
file is created after Hermes starts, restart Hermes to pick it up (or set
`RAGAMUFFIN_CONFIG` to an explicit path).

### Auth Token

The `auth_token` field in `ragamuffin.json` is written by the setup wizard
for convenience, but **prefer environment variables** (`RAGAMUFFIN_AUTH_TOKEN`)
for secrets. Config files in the filesystem are less secure than profile-level
`.env` files managed by Hermes.

---

## Development

```bash
# Run tests (no live Ragamuffin required)
cd adapters/hermes-memory/
python -m pytest tests/ -v

# Run a specific test file
python -m pytest tests/test_config.py -v

# With coverage
python -m pytest tests/ --cov=. -v
```

### Adding a new tool

1. Add the JSON schema to `ALL_TOOL_SCHEMAS` in `__init__.py`
2. Implement the handler method on `RagamuffinMemoryProvider`
3. Add tests in `tests/test_tools.py`
4. Add a brief description to the table above

---

## License

Same as Ragamuffin — MIT.
