# SPEC — v0.9.x: Cross-File Link Index

**Status:** Draft — ready for review
**Confidence:** High — design is clear, codebase inventory complete
**Issues:** #314
**Dependencies:** None (independent of #317, independent of #553)

---

## Overview

Ragamuffin currently treats every chunk as a floating island. Semantic search
finds related content, but there's no structural connectivity — an agent
reading about "deploy pipeline" can't discover which other files reference it.

This spec adds a lightweight link index extracted during indexing. It captures
explicit structural links (wikilinks `[[Page Name]]`, path references, tag
groupings) in a SQLite table and exposes them through three new endpoints.

No graph database. No entity extraction. Only what authors explicitly wrote.

---

## Approach

During chunking, the indexer passes through the raw text before splitting and
extracts three types of links:

1. **Wikilinks** — `[[Page Name]]` or `[[path/to/file]]` references, including
   display-text variants `[[target|display text]]`
2. **Path references** — explicit mentions of vault paths matching known patterns
   like `_house/infra/system-map.md` or `docs/architecture.md`
3. **Tag-based clusters** — files sharing identical tag sets form implicit groups

Links are stored in the existing SQLite logstore under a new `link_index` table.

---

## New Database Table

In `internal/logstore/schema.go` — add to `logstore.db`:

```sql
CREATE TABLE IF NOT EXISTS link_index (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_path TEXT NOT NULL,        -- vault path of referring file
    target_path TEXT NOT NULL,        -- vault path of referenced file
    link_type TEXT NOT NULL,          -- 'wikilink' | 'path_ref' | 'tag_cluster'
    context TEXT,                     -- first 200 chars of surrounding text
    vault TEXT NOT NULL DEFAULT '',   -- vault scope (empty = global)
    created_at TEXT NOT NULL          -- ISO 8601
);

CREATE INDEX IF NOT EXISTS idx_link_source ON link_index(source_path);
CREATE INDEX IF NOT EXISTS idx_link_target ON link_index(target_path);
CREATE INDEX IF NOT EXISTS idx_link_vault ON link_index(vault);
```

**Migration:** Run `CREATE TABLE IF NOT EXISTS` on server startup — no data
migration needed (existing data is unaffected; new indexing writes links).

---

## Indexer Changes

### `internal/indexer/chunker.go`

After the raw text is read (before chunking), add a link extraction pass:

```go
type Link struct {
    Target     string // resolved vault path
    RawText    string // the original [[wikilink]] or path mention
    Context    string // first 200 chars of surrounding paragraph
    LinkType   string // "wikilink" | "path_ref" | "tag_cluster"
}

func ExtractLinks(rawText string, sourcePath string, knownPaths []string) []Link
```

The extractor uses regex patterns:
- Wikilinks: `\[\[([^\]|]+)(?:\|([^\]]+))?\]\]` → capture group 1 is target,
  group 2 is optional display text
- Path refs: scan for strings matching known vault paths (`knownPaths` — loaded
  from vault metadata on startup)
- Tag clusters: the indexer already extracts tags from frontmatter — after
  indexing a file, check if its tag set matches any existing file's tag set

### `internal/indexer/indexer.go`

After chunk insertion succeeds, write extracted links to the logstore:

```go
func (idx *Indexer) writeLinks(ctx context.Context, vault string, sourcePath string, links []Link) error
```

This is a fire-and-forget write — failure to write links does NOT fail the
indexing operation (links are enrichment, not primary data). Log the error
and continue.

### Dependency Injection

The indexer needs access to the logstore's `link_index` table. Add a
`LinkIndexWriter` interface to keep the dependency contract clean:

```go
type LinkIndexWriter interface {
    WriteLinks(ctx context.Context, vault string, links []LinkRecord) error
    Close() error
}
```

The logstore already has a SQLite connection — implement `LinkIndexWriter` on
the `logstore.LogStore` or create a thin wrapper.

---

## New Endpoints

All endpoints are read-only. No authentication required (auth is per-request
via the existing middleware).

### `GET /v1/links?path=<vault-path>`

Returns all outbound links from the specified file:

```json
{
  "path": "_house/infra/system-map.md",
  "links": [
    {
      "target": "_house/philosophy/manifesto.md",
      "type": "wikilink",
      "context": "... See also [[manifesto]] for the north star ..."
    },
    {
      "target": "_house/infra/n8n-architecture.md",
      "type": "path_ref",
      "context": "... configured in `_house/infra/n8n-architecture.md` ..."
    }
  ]
}
```

404: path not found in the link index (empty result, not an error).

### `GET /v1/links/backlinks?path=<vault-path>`

Returns all inbound links TO the specified file:

```json
{
  "path": "_house/infra/system-map.md",
  "backlinks": [
    {
      "source": "_house/infra/deploy-pipeline.md",
      "type": "wikilink",
      "context": "... The [[System Map]] documents the full topology ..."
    }
  ]
}
```

### `GET /v1/links/graph?path=<vault-path>&depth=2`

Returns a breadth-limited link graph. Depth 1 = direct links. Depth 2 = links
of links. Max depth 5 (same as fact graph).

```json
{
  "path": "_house/infra/system-map.md",
  "depth": 2,
  "nodes": [
    {"path": "_house/infra/system-map.md", "type": "source"},
    {"path": "_house/philosophy/manifesto.md", "type": "wikilink"},
    {"path": "_house/infra/n8n-architecture.md", "type": "path_ref"},
    {"path": "_house/philosophy/values.md", "type": "wikilink"}
  ],
  "edges": [
    {"source": "system-map.md", "target": "manifesto.md", "type": "wikilink"},
    {"source": "system-map.md", "target": "n8n-architecture.md", "type": "path_ref"},
    {"source": "manifesto.md", "target": "values.md", "type": "wikilink"}
  ]
}
```

Implementation: BFS traversal using the link_index table, up to `depth` hops.
Let `link_index` do the heavy lifting with indexed queries on `source_path`/`target_path`.

### Vault-Scoped Variants

All three endpoints also exist under `/vault/{name}/v1/links*` for multi-tenant
deployments. Same behavior, filtered by `vault` column in the link_index table.

Registered in `server.go` alongside the existing vault-prefixed routes.

---

## /recall Enrichment

The `/v1/recall` and `/vault/{name}/v1/recall` responses gain an optional
`links` field when a match's source file has outbound links in the index:

```json
{
  "query": "deploy pipeline",
  "results": [
    {
      "chunk": "...",
      "score": 0.87,
      "source": "_house/infra/deploy-pipeline.md",
      "links": [
        {"target": "_house/infra/ci-config.md", "type": "wikilink"},
        {"target": "_house/infra/rollback.md", "type": "path_ref"}
      ]
    }
  ]
}
```

Add a `?enrich_links=true` query parameter to `/v1/recall`. Default: `false`
(backward compatible — existing agents see no change).

---

## Files to Create/Modify

| File | Change |
|---|---|
| `internal/logstore/schema.go` | Add `link_index` table to schema |
| `internal/logstore/links.go` | **New** — `LinkIndexWriter` implementation, CRUD methods |
| `internal/indexer/chunker.go` | Add `ExtractLinks` function, regex patterns |
| `internal/indexer/link_extractor.go` | **New** — link extraction logic, tests |
| `internal/indexer/indexer.go` | Call `writeLinks` after chunk insertion |
| `internal/server/handlers.go` or `internal/server/links.go` | **New** — three link query handlers |
| `internal/server/server.go` | Register `/v1/links*` and `/vault/{name}/v1/links*` routes |
| `internal/server/recall.go` | Add `?enrich_links=true` parameter, optional links in response |
| `smoke_test.sh` | Add curl smoke tests for all three endpoints |

---

## Configuration

No new env vars. The link index always runs when the indexer is active (no
opt-in/opt-out — the cost is negligible).

---

## Testing Requirements

- **Unit tests for `ExtractLinks`:** wikilink parsing (`[[Page]]`, `[[path|text]]`),
  path ref matching, tag cluster detection, edge cases (malformed brackets,
  empty content)
- **Unit tests for `link_index` CRUD:** write, read outbound, read inbound,
  BFS graph traversal to depth 5
- **Integration tests:** index a file with wikilinks, query `/v1/links?path=`,
  verify links are returned correctly
- **Smoke tests:** curl each endpoint, verify JSON structure

---

## Breaking Changes

**None.** All new endpoints. `?enrich_links=false` is the default for `/recall`.

---

## Resilience

- Link write failure during indexing is logged and non-fatal — indexing succeeds
  regardless
- `/v1/links*` endpoints return empty arrays for unknown paths (not 404 errors)
- Graph traversal has a depth limit of 5 (configurable at query time, capped at
  server level to prevent runaway queries)
