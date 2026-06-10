# Tiered Context Loading — Specification v0.1

## Overview

Add a `detail` parameter to `/recall` (and the MCP `ragamuffin_recall` tool) that
controls how much content is returned per result. Three levels:

- **L0** — header only (heading + key + score)
- **L1** — header + first paragraph (summary + key + score)
- **L2** — full chunk text (current behavior, unchanged default)

This lets agents search wide at low cost, then drill into specific chunks on
demand using the returned `chunk_id`.

## Design Principles

1. **No extra storage costs.** L0 uses the existing `header` payload field.
   L1 stores the first paragraph as a new payload field `first_paragraph`,
   populated during indexing. No new collections, no separate index.
2. **No LLM calls.** Summaries are extracted text, not generated. The chunker
   already knows paragraph boundaries. `first_paragraph` is the text up to the
   first double-newline or the first N chars (whichever comes first).
3. **Drill-down by ID.** Every chunk already has a deterministic `point_id`
   derived from `source_file:chunk_index`. We expose it as `chunk_id` in the
   response so agents can fetch specific chunks at L2 without re-running the
   full semantic search.

## Endpoint Changes

### POST /recall

New optional field in the request body:

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `detail` | string | no | `"l2"` | `"l0"`, `"l1"`, or `"l2"` |

#### L0 Response

```json
{
  "results": [
    {
      "chunk_id": "a1b2c3d4-e5f6-...",
      "source_file": "contractors/rates.md",
      "header": "## Review Cycle",
      "chunk_index": 3,
      "score": 0.87,
      "file_last_updated": "2026-05-09T10:21:13Z"
    }
  ],
  "top_score": 0.87
}
```

The `text` field is omitted. Only `chunk_id`, `source_file`, `header`,
`chunk_index`, `score`, and `file_last_updated` are returned.

#### L1 Response

```json
{
  "results": [
    {
      "chunk_id": "a1b2c3d4-e5f6-...",
      "source_file": "contractors/rates.md",
      "header": "## Review Cycle",
      "first_paragraph": "Contractor rates are reviewed quarterly...",
      "chunk_index": 3,
      "score": 0.87,
      "file_last_updated": "2026-05-09T10:21:13Z"
    }
  ],
  "top_score": 0.87
}
```

The `text` field is omitted. `first_paragraph` replaces it. This is ~50-100 tokens
per result — enough to gauge relevance without carrying the full chunk.

#### L2 Response

Current behavior. `text` field returns the full chunk. Unchanged.

### GET /v1/chunks/{chunk_id}

New endpoint. Fetch a single chunk by its deterministic chunk_id (UUID format).

**Request:**
```
GET /v1/chunks/a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d
```

**Response:**
```json
{
  "chunk_id": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
  "source_file": "contractors/rates.md",
  "header": "## Review Cycle",
  "text": "Contractor rates are reviewed quarterly by the finance team...",
  "chunk_index": 3,
  "file_last_updated": "2026-05-09T10:21:13Z",
  "score": null
}
```

Returns `404 NOT_FOUND` if the point doesn't exist in Qdrant.

**Error response:**
```json
{
  "error": true,
  "code": "NOT_FOUND",
  "message": "chunk with ID a1b2c3d4-e5f6-... not found"
}
```

## Indexer Changes

In `internal/indexer/indexer.go`, during chunk creation:

1. **Store `first_paragraph`** in the Qdrant payload. Extract from the chunk text:
   - Find the first double-newline (`\n\n`) or paragraph break
   - Take everything up to that break, capped at 200 characters
   - Store as `first_paragraph` in the point payload (same as `header`)

```go
// In the chunk → payload mapping (around line 270):
"first_paragraph": {Kind: &pb.Value_StringValue{StringValue: c.FirstParagraph()}},
```

The `chunk` struct needs a `FirstParagraph()` method (or the field can be extracted
during chunking and stored directly — simpler, no dependency on the chunker's
internal state).

2. **No changes to point_id scheme.** It's already deterministic from
   `source_file:chunk_index`. The UUID is reconstructable by any client that
   knows the source file and chunk index, but the endpoint makes it convenient.

## Handler Changes

### `recallResult` struct

Add `chunk_id` field (always populated), add `detail` to request struct.

```go
type recallRequest struct {
    Query          string        `json:"query"`
    TopK           int           `json:"top_k"`
    ScoreThreshold float64       `json:"score_threshold"`
    SourceFilter   string        `json:"source_filter"`
    Filters        *recallFilters `json:"filters,omitempty"`
    Detail         string        `json:"detail"`    // NEW: "l0", "l1", or "l2" (default)
}

type recallResult struct {
    ChunkID         string  `json:"chunk_id"`           // NEW
    Text            string  `json:"text,omitempty"`      // omit if empty
    SourceFile      string  `json:"source_file"`
    Header          string  `json:"header"`
    FirstParagraph  string  `json:"first_paragraph,omitempty"` // NEW
    ChunkIndex      int     `json:"chunk_index"`
    Score           float32 `json:"score"`
    FileLastUpdated string  `json:"file_last_updated"`
}
```

### `handleRecall` logic

After mapping results from Qdrant but before writing the response:

```
if detail == "l0":
    drop text and first_paragraph from each result
    keep chunk_id, header, source_file, chunk_index, score, file_last_updated

if detail == "l1":
    drop text from each result
    keep first_paragraph

if detail == "l2" (default or absent):
    current behavior, unchanged
```

The `chunk_id` field is populated from the point's UUID — reconstruct it using
the existing `pointID` function or read it from the point's `Id` field.

### `/v1/chunks/{chunk_id}` handler

New handler that:
1. Validates the chunk_id is a valid UUID
2. Parses it back to a Qdrant UUID point ID
3. Retrieves the point by ID from Qdrant (single point lookup, not a scroll)
4. Returns the full chunk payload as a response
5. Returns 404 if not found

```go
func (s *Server) handleChunkGet(w http.ResponseWriter, r *http.Request) {
    chunkID := r.PathValue("chunk_id")
    if chunkID == "" {
        writeError(w, 400, "INVALID_REQUEST", "chunk_id is required")
        return
    }
    // Parse UUID → Qdrant point ID
    pointID := parseUUIDPointID(chunkID)
    // Fetch point by ID
    point, err := s.qdrantFor(ctx).Get(ctx, pointID)
    // ...build response
}
```

## MCP Tool Changes

For the `ragamuffin_recall` MCP tool, add a `detail` parameter with the same
semantics. When detail is `l0` or `l1`, the tool description should note that
`text` will be empty and `first_paragraph` or `header` should be used instead.

The `ragamuffin_get_chunk` tool (new) wraps `GET /v1/chunks/{chunk_id}`.

## Agent Workflow (the pattern this enables)

```python
# Step 1: Search wide at L0 — costs ~15 tokens per result
results = await recall("contractor rate policies", detail="l0")
# Returns: [{chunk_id, header, score}]

# Step 2: Pick promising results by header
targets = [r for r in results if "review" in r.header.lower()][:3]

# Step 3: Fetch specific chunks at L2 — costs full chunk text
full = [await get_chunk(t.chunk_id) for t in targets]
# Returns: [{chunk_id, source_file, header, text, ...}]
```

Two calls instead of two full searches. The chunk_id lookup is a point retrieval,
not a vector search — it's essentially free.

## Phase 2 Precedent (for reference)

Knowledge graph traversal at `GET /v1/facts/{key}/graph?depth=N` **already exists**
in `factgraph.go`. It does BFS traversal of `supersedes`, `refines`, `contradicts`,
`supports` relationships including reverse edges. Depth is configurable (0-5).

The adjacency-list-at-depth-1 approach described in the original proposal
is what `depth=1` already returns — the node + its immediate neighbors in all
directions. No new work needed there, just documentation and smoke tests.

## Rollback

If tiered loading causes issues:
- Revert the `detail` param handling in `handleRecall`
- Revert the `first_paragraph` payload field in the indexer
- Revert the `chunk_id` field in `recallResult`
- Remove the `/v1/chunks/{chunk_id}` route

The `first_paragraph` data in existing Qdrant points is harmless — it's
just an unused payload field. No migration needed.

## Rejected Alternatives

**LLM-generated summaries for L1.** Rejected because it adds latency, cost,
and cold-start problems (no summaries for existing chunks). The heading-based
approach is zero-cost and available immediately for all chunks.
