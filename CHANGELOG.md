# Changelog

## v0.9.6 (2026-07-18)

### Features
- **MCP tool surface expansion (33 tools)**: Fact operations split into discrete tools
  (`ragamuffin_fact_get`, `_put`, `_list`, `_delete`, `_graph`, `_history`, `_provenance`).
  New graph tools (`ragamuffin_graph_entity`, `_edges`, `_communities`), context
  (`ragamuffin_context_bundle`, `_dialectic`, `_peer_list`, `_briefing`, `_changes`),
  review (`ragamuffin_review`, `_contradictions`), hybrid search (`ragamuffin_hybrid_search`),
  and utilities (`ragamuffin_links`, `_status`, `_chunks`, `_turn_append`, etc.).
  Backward-compatible: legacy `ragamuffin_facts` still dispatched. (#858)
- **MCP session-end notification**: `notifications/session_end` auto-finalizes sessions,
  builds structured summaries, extracts decision/conclusion facts from assistant turns,
  and marks sessions finalized. Idempotent. (#861)
- **Auto-provision agent vaults**: Agent vaults created on first MCP tool dispatch —
  no manual `POST /vaults` needed. Detected by vault prefix (e.g., `agent::`). (#859)
- **SDK packages**: Reusable MCP client libraries for Node.js (`sdks/ragamuffin-client-js/`)
  and Python (`sdks/ragamuffin-client-py/`). Zero external dependencies. (#860)
- **OpenClaw MCP rewrite**: Plugin connects via JSON-RPC 2.0 to `/mcp` instead of REST,
  dynamically discovering all 33 server tools. Replaces 3 hardcoded tools. (#859)
- **Adapter conformance test suite**: `tests/mcp_conformance_test.sh` — standard MCP
  integration tests for any adapter. (#862)
- **`ragamuffin_dialectic` tool**: Multi-pass reasoning prompts (cold analytical, warm
  synthetic, hot evaluative). Depth 1–3. Mirrors Hermes adapter's dialectic logic. (#862)
- **`ragamuffin_changes` tool**: Temporal awareness — vault activity over time. Shows
  recent log events and facts sorted by recency. (#862)
- **Documentation rewrite**: `docs/integration/memory-provider-api.md` rewritten MCP-first
  with full 33-tool catalog. REST documented as underlying transport. (direct)
- **Auto session-to-fact storage (Hermes adapter)**: `on_session_end` auto-extracts
  decisions, conclusions, config, and preferences from transcripts. Deduplicated by
  deterministic key. (#793)

### Bug Fixes
- **Lifecycle field preservation (REST + MCP)**: `POST /v1/facts` re-upsert no longer
  resets status, supersedes, refines, contradicts, supports, or access count. (#853)
- **Lifecycle field preservation (service.doFactsUpsert)**: MCP `ragamuffin_fact_put`
  now carries forward all lifecycle fields matching the REST path. (#863)
- **MCP nil dereferences**: Guarded `s.logStore`, `s.qdrantFor()`, `s.embeddingFor()`,
  `s.llmFor()`, and `s.facts` for nil across all MCP handlers. (#851)
- **Louvain community detection**: Self-loop weight now included in modularity gain
  for the stay candidate, preventing incorrect community splits at higher aggregation
  levels. (#855)
- **Proof-of-decay fields computed live**: Accessibility and EffectiveConfidence are
  computed via half-life formula rather than stamped on recall. (#846)
- **Reconsolidation no longer a no-op**: Facts now stamped on semantic recall path,
  making reconsolidation-on-recall functional. (#844)
- **Gist ID determinism**: UUIDv5 from gist key prevents duplicate gist points across
  consolidation runs. (#845)
- **Multi-query preserves lexical search**: Multi-query fan-out restricted to dense
  mode; hybrid/sparse use single rewrite with lexical preserved. (#847)
- **Graph correctness**: Entity dedup via `LOWER()`; temporal bounds normalized to
  RFC3339 UTC; supersession clamps prior edge's `valid_until` for disjoint intervals. (#848)
- **Shutdown ordering**: Consolidation worker stopped before logStore close. (#850)
- **Indexer data safety**: `indexFile` keeps delete-before-upsert pattern with
  documentation; `Ingest` now calls `DeleteBySource` before upserting; `chunkCount`
  tracks per-file counts for accurate stats. (#852)
- **LogStore double-close removed**: Signal handler no longer calls `logStore.Close()`
  — deferred close handles it. Prune context cancellable during shutdown. (#854)
- **`envBool` silent fallback warning**: Added warning for unrecognized bool values. (direct)
- **Review endpoint**: `min_confidence` renamed to `max_confidence` to match Lt semantics. (#856)

### Documentation
- **MCP-first integration guide**: Complete rewrite with 33-tool catalog, MCP session-end
  lifecycle, per-harness config reference, and REST-as-transport documentation. (direct)
- **CHANGELOG backfill**: All missing v0.9.0–v0.9.5 entries consolidated into release history.
- **SPEC.md MCP section**: Full tool catalog, session-end notification, auto-provisioning docs.
- **AGENTS.md**: Version table updated through v0.9.x; `sdks/` in project structure.
- **ROADMAP.md**: All v0.6–v0.9 items marked done; v0.9.x MCP expansion documented.
- **README.md**: Tool list updated to 33; MCP SDK and session-end notification added.
- **OpenAPI spec**: Version bumped to 0.9.6; MCP stub expanded.
- **Archive specs**: SPEC-MCP.md, SPEC-v0.5.md, SPEC-semantic-fact-search.md, and
  SPEC-temporal-awareness-complete.md updated with Done status banners and current
  implementation notes.

## v1.0.0-rc.1

### Features
- **Hermes memory adapter Phase 1**: Peer card abstraction, `ragamuffin_profile`/`context`/`learn`/`search` tools, `reasoning_effort` param, `ragamuffin.json` config loading. (#688, #749)
- **Semantic fact search**: Full-text + semantic search across facts via `/v1/facts?prefix=`. Qdrant token-based matching with hash-based dedup. (#725)
- **Fact lifecycle subsystem**: Briefing endpoint (`GET /v1/briefing`) for returning agents, hybrid recall (`POST /v1/hybrid`), resolvable provenance links on facts (`provenance` payload field). (#708, #709, #710)
- **Read tracking**: Facts now track `read_count`, `last_read_at`, unread review reason for facts never read or not read in 30+ days. (#711)
- **Fact mode routing**: `RAGAMUFFIN_FACTS_MODE` env var — vault/global/both modes for fact CRUD routing. (#703)
- **Temporal awareness in `/ask`**: Parallel fact retrieval separated from document recall, chunk position metadata in synthesized context, temporal prompt improvements for better date-aware answers.
- **Chunking during ingest**: `--chunks` flag for splitting conversations during session ingestion.
- **Vault cleanup**: `--clean` flag for vault reset, unique per-run vault names in benchmark mode. (#724)
- **Separate timeouts for ingest vs ask**: `RAGAMUFFIN_INGEST_SERVER_TIMEOUT` and `RAGAMUFFIN_ASK_TIMEOUT` env vars. (#723)
- **Per-vault health stats**: `/health` now includes per-vault metrics. (#729)
- **CLI argument support**: `--help` and proper CLI argument parsing for runner. (#726, #727)
- **Procedural memory extraction**: Extracts step-by-step procedures from session traces, stored as facts with `type: procedure` payload. (#317)
- **Cross-file link index**: Links between related documents stored in Qdrant. Enriched recall with related chunks. (#314)
- **Benchmark v2.0 rewrite**: Reliable logging, checkpoint-based resume, circuit breaker, LLM judge replacing fuzzy matcher for scoring. (#647)
- **Benchmark Phase 1-2 suites**: 6 new benchmarks (rate-limit, large-vault, draft-audit, event-stream, LongMemEval, LoCoMo). (#647)
- **Optimized embedding**: Avoids redundant `EmbedSingle` in `appendFactContext` by caching embeddings. (#733)
- **Ingest pacing**: Configurable ingest delay between chunks (`--ingest-delay`), Qdrant health gate fire suppression for transient connection drops. (#748, #751)

### Bug Fixes
- **Security hardening (Phases 1-4)**: Critical-to-medium security fixes, CI hardening, security regression test suite, archived stale SPEC files. (#700, #701, #702)
- **CloudEvents fixes**: Payload validation checks envelope not inner data, emitter source set to `'ragamuffin'` in multi-tenant mode, vault file change events emit correctly, bare `/draft` and `/audit` routes registered in all modes. (#660, #661, #662, #669, #676)
- **Vault-scoped Qdrant client**: Fact CRUD writes now use vault-scoped Qdrant client instead of global client. (#656)
- **Benchmark route fixes**: Uses vault-prefixed routes for fact CRUD operations. (#654)
- **Rate-limit benchmark**: Handles disabled rate limiting and malformed request bodies gracefully. (#658)
- **Benchmark `list_vaults`**: Returns vault names not dicts. (#659)
- **Zero-vector reembed scanner**: Retries zero-vector embeddings with scanner and retry logic. (#672)
- **golang-jwt upgrade**: v5.2.1 → v5.2.2 (GO-2025-3553).
- **gofmt alignment**: All 55 Go files reformatted.
- **Miscellaneous**: CI test failures in procedural package, gofmt CI failure for trailing blank lines, Qdrant health gate fire suppression when `--ingest-delay > 0`. (#751)

### Documentation
- **README rewrite**: Complete rewrite with human-readable intro, tiered recall, fact extraction section, MCP tools list. (#679, #561)
- **API reference**: Full 13700-line reference covering all 40+ endpoints including health, vaults, facts CRUD + graph, sessions, documents, review queue, pruner, inbox, auth, MCP. (#565)
- **OpenClaw agent integration**: Reference agent implementation with tools, config example, and skill definitions. (#566)
- **Temporal awareness spec**: New SPEC-temporal-awareness-complete.md documenting temporal reasoning architecture.
- **Benchmarks README**: New file documenting benchmark harness usage, four configs (A-D), LongMemEval + LoCoMo, v2 architecture. (#566)
- **Archive cleanup**: 31 stale SPEC files from v0.x era moved to `specs/archive/`. (#702)
- **Various**: Docs for procedural memory extraction, cross-file link index, pr.go purpose, stale fact vector comments. (#683)

### Build & Testing
- **Test coverage expansion**: Facts handler tests (#675), MCP handler tests (#674), sessions handler tests (#673).
- **Consolidated CI build**: 5 merged PRs consolidated into one CI build for `:rolling` tag. (#440)
- **Security regression tests**: Dedicated test suite for security invariants. (#701)

## v0.9.0-rc.1

### Bug Fixes
- **conversation facts zero vectors**: `conversation.go` now calls `s.embedder.EmbedSingle()` instead of `make([]float32, FactsVectorSize)` before creating Qdrant points. Embedding failures fall back to zero vector with metadata preserved. (#558)
- **confidence scale mismatch**: Normalized LLM integer 1-10 confidence to float64 0.0-1.0 at storage layer (divide by 10, cap at 0.85 max, clamp at 0.0 min). Pruner's low-confidence threshold now correctly triggers. (#559)
- **context leak in documents.go extraction goroutine**: Changed `r.Context()` to `s.shutdownCtx` in the document extraction goroutine, matching the proven pattern from sessions.go. Prevents extraction from being cancelled when the HTTP handler returns. (#560)

### Documentation
- **README**: Updated with tiered recall (`mode` parameter), temporal filtering, fact extraction section, extraction config env vars, and MCP tools list. (#561)
- **DEPLOY.md**: Added extraction config section (`RAGAMUFFIN_EXTRACT_*`), auto-tune endpoint docs, pruner env vars in quick reference. (#562)
- **AGENTS_SKILL.md**: Added `mode`/`time_filter` to recall and ask, `auto_extract` on ingest and sessions, fact graph query example. (#563)
- **CHANGELOG**: Full history from v0.6.0 through v0.8.10 with accurate tag-based entries. (#564)
- **OpenAPI spec**: Expanded from memory-provider-only to full API surface: 40+ endpoints covering health, vaults, facts CRUD + graph, sessions, documents, review queue, pruner, inbox, auth, MCP, and all vault-prefixed routes. (#565)
- **Benchmarks README**: New file documenting benchmark harness usage, four configs (A-D), LongMemEval + LoCoMo, and v2 architecture. (#566)

### Build & Testing
- **Pruner integration tests**: Added 6 scan tests (stale, conflict, low-confidence, supersede, expired temporal, scheduler) against real Qdrant via `QDRANT_TEST_URL`. All gated behind `-test.short` and env var. (#309)
- **Build ldflags**: Added `Makefile` with `make build`/`make build-all` targets auto-detecting version from git. CI binary releases now pass `Version`, `Commit`, `BuildDate`, and `GoVersion` via ldflags. (#310)

## v0.8.10 (2026-06-07)

### Bug Fixes
- **Auto-provision vault on session create**: Sessions now auto-create their vault if it doesn't exist yet. (#547)
- **Swapped args in ask_in_vault call**: Fixed benchmark harness argument order. (#545)

## v0.8.9 (2026-06-07)

### Bug Fixes
- **Health handler nil Qdrant guard**: `/health` no longer panics when Qdrant is still connecting. (#543)

## v0.8.8 (2026-06-07)

### Bug Fixes
- **Multi-tenant zero-vault crash**: Fixed nil pointer during log store init when `Vaults` configuration set is empty. (#542)

## v0.8.7 (2026-06-07)

### Bug Fixes
- **Auto-provision crash when Vaults set is empty**: Guard against nil vault list during auto-provision. (#548)

## v0.8.6 (2026-06-07)

### Features
- **`/vault/{name}` routes in single-tenant mode**: Vault-prefixed routes now work even without full multi-tenant mode enabled. (#539)
- **Derived vault paths from VAULTS_ROOT**: Vault paths are optional in `RAGAMUFFIN_VAULTS` — they derive from `VAULTS_ROOT/<name>` when omitted. (#539)

## v0.8.5 (2026-06-07)

### Features
- **VaultsRoot + AutoProvisionVaults**: Multi-tenant mode auto-triggers when `VAULTS_ROOT` is set. Vaults auto-provision on first write. (#548)
- **Auto-extract on `/v1/documents`**: Documents can trigger fact extraction with `auto_extract: true`. (#502)
- **Auto-index session turns**: Sessions treated as per-turn memories, indexed into vault Qdrant collection. (#569)
- **Benchmark checkpoint**: Per-question progress file for resumable evaluation. (#481)
- **Temporal metadata**: `valid_from`/`valid_until` supported in benchmark data path. (#481)

### Bug Fixes
- **Missing meta arg in main.go Ingest call**: Fixed argument count mismatch. (#548)

## v0.8.4 (2026-06-07)

### Features
- **LongMemEval splitter script**: Stream-parses 277MB dataset into per-conversation files. (#481)
- **Batch recall**: `POST /v1/batch/recall` for multiple queries in a single request. (#498)
- **IngressDriver architecture**: `IngressDriver` interface with `FileWatcherDriver` and `APIIngestDriver` implementations. Pluggable ingest pipelines. (#500-#503)
- **`POST /v1/documents`**: Ingest documents via API with auto-extract. (#502)
- **`POST /v1/sessions/batch`**: Bulk session ingestion. (#504)
- **`POST /v1/vaults/{name}/clear`**: Reset a vault's Qdrant collection. (#505)
- **OIDC-Native Authentication with Authentik**: Full OIDC integration. (#508)
- **Tiered recall via `mode` parameter**: Auto-classify queries as RAG (`rag`), auto-detect (`auto`), or full-document (`full`). (#507)
- **MCP-to-REST adapter layer**: Refactored MCP gateway to reusable adapter pattern. (#492)
- **Review Queue Dashboard**: Single HTML page for reviewing flagged facts. (#509)
- **ZFS-Native Watcher**: `POST /v1/refresh` for manual re-index trigger without inotify. (#510)
- **Shared utility functions**: `writeError`, `writeJSON`, `IsIndexable` extracted as shared. (#511)

### Bug Fixes
- **Batch recall edge cases**: Parallel processing, error isolation, per-query timeout. (#498)
- **CI build failures**: Fixed review_test.go arg, unused imports. (#492)
- **LoCoMo speaker mapping**: Fixed speaker mapping and evaluator model config. (#481)
- **Duplicate ingress import**: Resolved chan direction mismatch. (#503)

### Refactoring
- **Shared utility functions**: `writeError`, `writeJSON`, `IsIndexable` extracted. (#511)
- **MCP-to-REST adapter**: Separated MCP handshake from REST dispatch. (#492)

## v0.8.3 (2026-06-06)

### Features
- **Benchmark harness**: LongMemEval + LoCoMo benchmark for 4 config variants (A-D). (#479)

### Bug Fixes
- **Startup context timeout**: Added 30s timeout to `ensureFactIndexes` and `migrateFacts` to prevent startup hangs. (#480)

### Documentation
- **SPEC.md audit**: Complete event types and PUT/PATCH `valid_from`/`valid_until` fields. (#478)

## v0.8.2 (2026-06-06)

### Bug Fixes
- **8 review findings**: Fixed issues from v0.8 code review. (#477)

## v0.8.1 (2026-06-05)

### Features
- **Automatic memory extraction**: LLM-powered fact extraction from conversation turns. Opt-in on sessions via `auto_extract: true`. Async execution with configurable cooldown and dedup. (#464)

## v0.8.0 (2026-06-05)

### Features
- **Temporal reasoning**: `valid_from`/`valid_until` on facts, `time_filter` on `/recall` and `/v1/facts` (`active`, `active_at:<RFC3339>`, or `all`). Pruner expired scan for time-bounded facts. (#463)

## v0.7.0 (2026-06-05)

### Features
- **Adaptive pruner thresholds**: Pruner thresholds auto-tune based on review resolution history via `GET /v1/pruner/auto-tune`. (#447, #448)
- **Fact knowledge graph**: `GET /v1/facts/{key}/graph` returns supersedes/superseded_by chains. (#448)
- **MCP `detail` param on recall**: Configurable result detail level (chunk metadata, headers, full text). (#447)
- **Webhook notifications for fact lifecycle**: CloudEvents v1.0 structured JSON on fact events. (#447)
- **`RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD`**: Configurable low-confidence flag threshold. (#447)
- **`ragamuffin_get_chunk` MCP tool**: Fetch individual chunks by Qdrant point ID. (#453)
- **Resolution logging**: Every review action logged with before/after state. (#456-#461)
- **`first_paragraph` in Qdrant payload**: Stored during indexing for richer snippet previews. (#443)
- **AGPL-3.0 license**: Replaced MIT with AGPL-3.0. NOTICE file added. (#440)
- **Mermaid diagrams**: ASCII art replaced with Mermaid for mobile-friendly rendering. (#439)

### Bug Fixes
- **Rate limit keys**: Fixed per-endpoint rate limiting to use correct bucket keys. (#456-#461)
- **License detection**: Replaced LICENSE with raw AGPL-3.0 text. (#440)
- **6 fixes from code review**: #435, #436, #437, #399, #423, #425. (#431, #438)
- **`markContradiction` fix**: Uses `GetPoints` instead of `ScrollFiltered`. (#406)
- **`PrunerSourceStaleInterval` env var binding**: Fixed env var wiring. (#434)
- **Helper migration**: Duplicated helpers → shared `qutil` package. (#428)
- **Auth + infra fixes**: #410, #411, #413, #414, #424. (#427)
- **Facts package fixes**: #408, #409, #412, #395, #416, #419, #420. (#426)

### Documentation
- **Env vars and endpoints**: Complete tables added. (#432, #433)
- **Mermaid diagrams**: Mobile-friendly rendering. (#439)
- **Fact graph and webhook docs**: Added to SPEC.md, DEPLOY.md, README.md. (#448, #454)

## v0.6.1 (2026-06-05)

### Bug Fixes
- **3 review nits**: Fixed code review findings #435, #436, #437. (#438)
- **`PrunerSourceStaleInterval`**: Fixed env var binding. (#434)
- **3 stragglers**: Fixed #399, #423, #425. (#431)
- **`markContradiction` fix**: Uses `GetPoints` instead of `ScrollFiltered`. (#406) (#430)
- **Docs nits**: go.mod, prune comment, coverage.out cleanup. (#429)
- **Phase 2 cleanup**: Migrated duplicated helpers to shared `qutil` package. (#428)
- **Auth + infra bug fixes**: #410, #411, #413, #414, #424. (#427)
- **Facts phase bug fixes**: #408, #409, #412, #395, #416, #419, #420. (#426)
- **Stale dev instruction file**: Removed.

### Documentation
- **Removed stale references**: CONFIDENCE_BOOST, `/vaults/:name/health`, port 8080. (#432)
- **Added missing env vars and endpoints**: SPEC.md. (#433)

## v0.6.0 (2026-06-05)
