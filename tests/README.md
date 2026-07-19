# Tests

## Integration Test

`tests/integration_test.sh` — spins up a fresh Ragamuffin instance with
embedded store (no Qdrant needed), runs the full MCP lifecycle end-to-end,
then cleans up.

```bash
# Basic (no embedding — 49 tests, 0 failures expected)
./tests/integration_test.sh

# Full (with embedding — tests recall, auto-provisioning)
export RAGAMUFFIN_EMBEDDING_API_KEY=sk-...
./tests/integration_test.sh

# Explicit binary path
./tests/integration_test.sh ./cmd/ragamuffin

# Custom port (default 8001)
RAGAMUFFIN_INTEGRATION_PORT=9999 ./tests/integration_test.sh
```

### What it covers (49 assertions)

| Section | Tests | Count |
|---------|-------|-------|
| MCP Protocol | initialize, tools/list (32 tools), input schemas, error handling | 28 |
| Auto-Provisioning | recall against uncreated vault (requires API key) | 2 |
| Session Lifecycle | create, turn_append, get, list | 5 |
| Fact CRUD | put, get, re-upsert (lifecycle fields), list, delete | 5 |
| Fact Graph | put two facts, graph traversal | 3 |
| Context & Discovery | context_bundle, peer_list, briefing, changes | 4 |
| Review Queue | review list | 1 |
| Status & Graph | stats, status, graph_communities, graph_edges | 4 |

### Known skips

- **Auto-provisioning recall**: requires `RAGAMUFFIN_EMBEDDING_API_KEY`
- **Fact delete verification**: the embedded store's `DeleteFiltered` only
  supports source_file filters. Works with Qdrant.

## Conformance Test

`tests/mcp_conformance_test.sh` — standalone MCP protocol conformance suite.
Assumes a running Ragamuffin instance.

```bash
MCP_HOST=localhost MCP_PORT=8000 ./tests/mcp_conformance_test.sh
```

## Smoke Test

`../smoke_test.sh` — comprehensive REST endpoint smoke test (1098 assertions).
Assumes a running Ragamuffin instance with data.

```bash
SMOKE_HOST=localhost SMOKE_PORT=8000 ../smoke_test.sh
```
