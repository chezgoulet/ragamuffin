# Changelog

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
