# Changelog

## v0.8.11 (2026-06-19) — Sprint

### Bug Fixes
- **path traversal in inbox handlers**: Added `validInboxID()` regex validation (`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`) to inbox handlers. `parseInboxFile()` returns empty on invalid IDs; handlers return 400 `INVALID_ID`. Rejects `../etc/passwd`, URL-encoded traversal, null bytes, and leading slashes. (#557)
- **conversation facts zero vectors**: `conversation.go` now calls `s.embedder.EmbedSingle()` instead of `make([]float32, FactsVectorSize)` before creating Qdrant points. Embedding failures fall back to zero vector with metadata preserved. (#558)
- **confidence scale mismatch**: Normalized LLM integer 1-10 confidence to float64 0.0-1.0 at storage layer (divide by 10, cap at 0.85 max, clamp at 0.0 min). Pruner's low-confidence threshold now correctly triggers. (#559)
- **context leak in documents.go extraction goroutine**: Changed `r.Context()` to `s.shutdownCtx` in the document extraction goroutine, matching the proven pattern from sessions.go. Prevents extraction from being cancelled when the HTTP handler returns. (#560)
- **provisionVault path nesting**: Fixed double path nesting by using `VaultsRoot/<name>` directly instead of `VaultsRoot/<name>/<name>`. (#570)
- **missing idx.Ingest on turn append**: `handleSessionCreate` and `handleTurnAppend` now call `idx.Ingest` after appending turns, ensuring session content is indexed in Qdrant. (#569)
- **stats nil Qdrant guard**: `/stats` handler now guards against nil Qdrant client to prevent panic during startup. (#552)
- **linkFactToChunks nil guard**: Protected goroutine against nil Qdrant client panic. (#567)
- **fallback to factsQc**: When no pre-configured vaults exist, fall back to the shared facts Qdrant client. (#556)
- **provisionVault basePath**: Fallback to `VaultsRoot` when vault base path is empty. (#548)
- **auto-provision vault on session create**: Sessions now auto-provision the vault if it doesn't exist. (#547)
- **multi-tenant zero-vault crash**: Fixed nil pointer during log store init when Vaults set is empty. (#542)
- **handleHealth nil qdrant guard**: Health check handler guards against nil Qdrant during startup. (#543)

### Features
- **Rolling Docker tag**: `:rolling` tag published on every merge to main. (#554)

## v0.8.3 (2026-06-15)

### Bug Fixes
- **Startup context timeout**: Added 30s timeout to `ensureFactIndexes` and `migrateFacts` to prevent startup hangs. (#480)
- **8 review findings**: Fixed code review findings from v0.8 code review. (#477)

### Features
- **Benchmark harness**: LongMemEval + LoCoMo for 4 config variants. Benchmark harness v2. (#479)

### Documentation
- **SPEC.md audit**: Complete event types, PUT/PATCH valid_from/valid_until fields documented. (#478)

## v0.8.2 (2026-06-12)

### Bug Fixes
- **8 review findings**: Fixed issues identified in v0.8 code review. (#477)

## v0.8.1 (2026-06-10)

### Bug Fixes
- **Startup context timeout**: Added 30s timeout to `ensureFactIndexes` and `migrateFacts` to prevent startup hangs. (#480)

### Features
- **Benchmark harness**: LongMemEval + LoCoMo benchmark for 4 config variants. (#479)

### Documentation
- **SPEC.md audit**: Complete event types and PUT/PATCH valid_from/valid_until fields. (#478)

## v0.8.0 (2026-06-08)

### Features
- **Temporal reasoning**: `valid_from`/`valid_until` fields on facts, `time_filter` parameter on `/recall` and `/facts` supporting `active`, `active_at:<RFC3339>`, or `all`. Pruner expired scan for time-bounded facts. (#463)
- **Automatic memory extraction**: LLM-powered fact extraction from conversation turns. Opt-in via `auto_extract: true` on sessions and documents. Async execution with configurable cooldown and dedup. (#464)
- **Auto-extract on `/v1/documents`**: Documents can now trigger fact extraction with `auto_extract: true`. (#502)
- **Auto-index session turns**: Sessions treated as per-turn memories, indexed into vault Qdrant collection for cross-session recall. (#569)
- **VaultsRoot + AutoProvisionVaults**: Multi-tenant mode auto-triggers when VaultsRoot is set. Vaults auto-provision on first write. (#548)
- **OIDC-Native Authentication with Authentik**: Full OIDC integration for Authentik identity provider. (#508)
- **`POST /v1/sessions/batch`**: Bulk session ingestion for batch processing. (#504)
- **`POST /v1/vaults/{name}/clear`**: Reset a vault's Qdrant collection data. (#505)
- **`POST /v1/refresh`**: Manual re-index trigger for the ZFS-native watcher. (#510)
- **Tiered recall via `mode` parameter**: Auto-classify queries as RAG (`rag`), auto-detect (`auto`), or full-document (`full`). Mode selection returned in `mode_used` field. (#507)
- **`POST /v1/batch/recall`**: Batch recall for multiple queries in a single request. (#498)
- **MCP-to-REST adapter layer**: Refactored MCP gateway to a reusable adapter pattern separating MCP handshake from REST dispatch. (#492)
- **Ingress driver architecture**: `IngressDriver` interface with `FileWatcherDriver`, `APIIngestDriver` implementations. Pluggable ingest pipelines. (#500-#503)
- **Review Queue Dashboard**: Single HTML page for reviewing flagged facts. (#509)
- **LoCoMo single-document data path**: Benchmark support for single-document evaluation format. (#481)
- **Per-question checkpoint**: Benchmark harness saves progress per-question for resumable evaluation. (#481)
- **LongMemEval splitter script**: Stream-parses 277MB datasets into per-conversation files. (#481)

### Bug Fixes
- **Batch recall**: Parallel batch processing, error isolation, per-query timeout. (#498)
- **Missing meta arg in main.go Ingest call**: Fixed argument mismatch. (#548)
- **LoCoMo speaker mapping**: Fixed speaker mapping and evaluator model config. (#481)
- **main.go duplicate ingress import**: Resolved duplicate import and chan direction mismatch. (#503)
- **CI build failures**: Fixed review_test.go arg and unused imports. (#492)
- **Resolved service.go build failures**: Fixed test file compilation errors. (#492)

### Refactoring
- **Shared utility functions**: Extracted `writeError`, `writeJSON`, `IsIndexable` as reusable shared functions. (#511)
- **MCP-to-REST adapter**: Separated MCP handshake from REST dispatch. (#492)

### Documentation
- **SPEC.md**: Updated with all new endpoints, env vars, and temporal filtering. (#478)
- **Benchmark README**: Added docs for LongMemEval and LoCoMo benchmarks. (#452)

## v0.7.0 (2026-06-06)

### Features
- **Adaptive pruner thresholds**: Pruner thresholds auto-tune based on review resolution history via `GET /v1/pruner/auto-tune?dry_run=true`. Analyzes accept rates to recommend optimal thresholds. (#447, #448)
- **Fact knowledge graph**: `GET /v1/facts/{key}/graph` returns supersedes/superseded_by chains for any fact. Global and vault-scoped routes. (#448)
- **MCP `detail` param on recall**: Configures result detail level (chunk metadata, headers, full text) per recall query. (#447)
- **Webhook notifications for fact lifecycle**: CloudEvents v1.0 structured JSON pushed to configured webhook URL on fact events. (#447)
- **`RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD`**: Configurable threshold for low-confidence fact flagging. (#447)
- **`ragamuffin_get_chunk` MCP tool**: Fetch individual chunks by Qdrant point ID. (#453)
- **Resolution logging**: Every review action logged with before/after state for auditability. (#456-#461)
- **`first_paragraph` in Qdrant payload**: Stored during indexing for richer snippet previews. (#443)
- **AGPL-3.0 license**: Replaced MIT with AGPL-3.0. NOTICE file added for copyright attribution. (#440)
- **Mermaid diagrams**: ASCII art replaced with Mermaid for mobile-friendly README rendering. (#439)

### Bug Fixes
- **Rate limit keys**: Fixed per-endpoint rate limiting to use correct bucket keys. (#456-#461)
- **License detection**: Replaced LICENSE with raw AGPL-3.0 text to fix GitHub detection. (#440)
- **6 fixes from code review**: #435, #436, #437, #399, #423, #425. (#431, #438)
- **`markContradiction` GetPoints fix**: Uses `GetPoints` instead of `ScrollFiltered` for reliable conflict detection. (#406)
- **`PrunerSourceStaleInterval` env var binding**: Fixed env var wiring. (#434)
- **Helper migration**: Moved duplicated helpers to shared packages (`qutil`). (#428)
- **Auth + infra fixes**: #410, #411, #413, #414, #424. (#427)
- **Facts package fixes**: #408, #409, #412, #395, #416, #419, #420. (#426)

### Documentation
- **Env vars and endpoints**: Complete env var table, missing endpoint docs added. (#432, #433)
- **Mermaid diagrams**: README diagrams now render properly on mobile. (#439)
- **Fact graph and webhook docs**: Added to SPEC.md, DEPLOY.md, README.md. (#448, #454)

## v0.6.1 (2026-06-05)

### Bug Fixes
- **Pruner source-stale interval**: Fixed env var binding for `RAGAMUFFIN_PRUNER_SOURCE_STALE_INTERVAL`. (#434)
- **Facts-phase fixes**: Batch of fact lifecycle fixes (#408, #409, #412, #395, #416, #419, #420). (#426)
- **Auth + infra fixes**: 4 fixes for auth middleware and infrastructure (#410, #411, #413, #414, #424). (#427)
- **Helper consolidation**: Migrated duplicated helpers to shared `qutil` package. (#428)
- **`markContradiction` fix**: Uses `GetPoints` instead of `ScrollFiltered` for reliable conflict scanning. (#406)
- **License file replacement**: Raw AGPL-3.0 text for GitHub detection. (#440)

### Documentation
- **Removed stale references**: Cleaned up CONFIDENCE_BOOST, /vaults/:name/health, port 8080 references. (#432)
- **Missing env vars and endpoints**: Added to SPEC.md. (#433)

## v0.6.0 (2026-06-05)

### Features
- **OIDC-native authentication with discovery flow**: Full OIDC integration with automatic provider discovery.
  Supports `none`, `api_key`, `jwt`, and `oidc` auth modes. JWT verification via JWKS endpoint.
  New endpoints: `/v1/auth/check` for token validation. (#333)
- **Per-vault fact isolation with dedicated collections**: Facts stored in physically separate Qdrant
  collections (`ragamuffin_facts` by default, `ragamuffin_{vault}_facts` per vault). Prevents
  metadata-filter data leakage across agents. `FactsCollectionFor()` helper. (#331)
- **Configurable embedding dimensions with auto-detect probe**: Embeddings auto-detect their dimension
  from the configured model response. Configurable via `RAGAMUFFIN_EMBEDDING_DIMS`. Separate
  `RAGAMUFFIN_CHUNK_VECTOR_SIZE` for doc chunks vs facts. (#330)
- **Versioned supersede with integer version field**: Facts now carry a `version` integer field.
  Supersede action increments version. `parseVersionFromKey()` for version-aware fact key parsing. (#332)
- **Restore-from-snapshot detection**: On startup, compares current index against the last restored
  snapshot. Detects drift and reports via `/audit`. Configurable via `RAGAMUFFIN_RESTORE_MISMATCH_THRESHOLD`. (#338)
- **Graceful Qdrant lifecycle with reconnection loop**: Automatic Qdrant reconnection on connection
  loss. Exponential backoff with jitter. Connection health exposed via `/health` and `/metrics`. (#337)
- **Webhook event emitters for fact lifecycle and pruner**: New `EventBroker` dispatches fact lifecycle
  events (created, updated, superseded, rejected, confirmed) to configured webhook URL and SSE stream.
  Fact status changes emit CloudEvents. (#334)
- **Fact-to-chunk bridge with source stale scan**: Facts can reference source chunks via `source_file`
  payload field. Pruner scans for facts whose source chunk has been deleted or modified. (#335)
- **Review queue with per-fact supersede**: `POST /v1/review?key=<fact_key>&action=supersede` with
  `new_key`/`new_value` support. Non-destructive — old fact stays as `superseded` for audit trail.
  Also supports `confirm`, `reject`, `reclassify` actions. (#332)
- **SSE event stream**: `/events` endpoint provides real-time Server-Sent Events for fact lifecycle
  and broker notifications. Auto-reconnect compatible. (#334)
- **Sessions API**: `POST /v1/sessions` to create conversation sessions, `GET /v1/sessions/{id}` for
  retrieval. Session-based ingest routing for agent memory. (#327)

### Configuration
- **30+ new environment variables** across auth, pruner, rate limiting, facts, chunking, and events.
  See updated SPEC.md for the full reference table. (#330-#338)

### Documentation
- **SPEC.md**: Rewrote Config Reference section with complete env var table including all new v0.5/v0.6
  variables. Updated Non-Goals for v0.6. (#343)
- **DEPLOY.md**: Added auth setup guide, pruner configuration, multi-tenant vault setup,
  snapshot restore procedure, and webhook event configuration. (#343)
- **AGENTS_SKILL.md**: Added review queue, SSE events, OIDC auth, webhook emitters, fact-to-chunk
  bridge, and updated endpoint cheat sheet. (#343)
- **README.md**: Updated Two Patterns diagram for v0.6 features. Added auth headers to curl examples.
  Bumped version references to 0.6. (#343)

### Bug Fixes
- **TestReviewPost_Supersede**: Added missing `version` and `superseded_by` fields to test helper
  point to prevent `migrateFacts()` from triggering an extra Upsert during `New()`. (#343)

## v0.5.0 (2026-05-22)

### Features
- **Fact lifecycle management**: Automated pruner scans for stale facts, fact
  conflicts, supersession chains, and low-confidence facts. Facts are flagged
  as `needs_review` and can be resolved via the review queue API.
- **Review queue API**: `GET /v1/review` lists flagged facts with pagination
  and filtering by reason type. `POST /v1/review` resolves items via
  confirm, supersede, reject, or reclassify actions.
- **Fact update endpoints**: `PUT /v1/facts` (full replace) and
  `PATCH /v1/facts` (partial update) for programmatic fact management.
- **Agent memory backend**: Per-agent Qdrant vaults, session ingest stubs,
  vault provisioning, cross-agent recall. OpenClaw + Hermes plugin adapters
  for harness integration.
- **Audit endpoint**: `POST /v1/audit` with configurable checks
  (stale, semantic_conflict, gap, duplicate) and LLM-powered conflict
  detection.
- **Knowledge graph**: Entity-relationship graph via `/graph` and
  `/vault/{name}/graph` with BFS traversal up to depth 5.
- **SSE streaming**: Real-time event stream at `/events` for fact lifecycle
  notifications.
- **Embedded web UI**: Built-in web dashboard at `http://localhost:8000/` for
  vault browsing and basic operations.

### Bug Fixes
- **Pruner data-loss prevention**: `updateFactStatus` and `updateFactPayload`
  now use Qdrant's `SetPayload` API for field-level partial updates, preventing
  payload field loss without requiring read-then-merge.
  - Added `SetPayload` to `FactStore` interface, `Client`, and `MockQdrant`.
- **.env.example**: Added missing `RAGAMUFFIN_QDRANT_URL` to Required section.
- **Hermes plugin**: Fixed `AttributeError` crash in `_create_vault` — removed
  uninitialized `self._vault_path` reference.
- **Graph depth alignment**: REST default changed to 1, both REST and MCP now
  enforce min 1 (removed unreachable `depth == 0` fast path). MCP tool
  description now matches (1-5, default 1).
- **PATCH TTL → expires_at_unix**: TTL updates now set `expires_at_unix`
  in addition to `expires_at`, fixing stale-scan misses.
- **Review reclassify status**: Reclassification now sets status to `active`
  as documented.
- **Review supersede response fix**: Eliminated potential double-write to HTTP
  response when superseding with new_value.
- **Ingest body size limit**: Added 10 MB `MaxBytesReader` limit to
  `POST /v1/ingest`.
- **Graph depth alignment**: Both REST and MCP graph handlers now support
  depth 1–5 (was 0–3 on REST).
- **Vault creation validation**: `POST /vaults` now validates vault name
  via `config.ValidVaultName()` and removes stale directories on concurrent
  creation conflicts.
- **Auth middleware for MCP**: `/mcp` and `/events` added to PublicPaths
  for protocol-level auth compatibility.
- **Rate limit Retry-After format**: Changed from RFC1123 date to integer
  seconds for client simplicity.
- **Review stats fetch all**: Changed limit from 0 (default 10) to 100000
  to return all flagged facts.
- **Low-confidence filter**: Changed from `Lt` with epsilon to `Lte` on
  threshold, fixing off-by-one.
- **Go module directive**: `go 1.25.0` → `go 1.25` (patch version removed).

### Plugins
- **Hermes adapter**: Added `_create_vault` fallback when vault doesn't exist.
- **OpenClaw adapter**: Fixed facts endpoint from vault-prefixed
  `/vault/{name}/v1/facts` to global `/v1/facts`. Default port aligned to
  `8000` (was `8080`).
- **Plugin port alignment**: Hermes plugin default endpoint changed from
  `8080` to `8000`.

### Documentation
- Updated all `/v1/recall` references to `/vault/{name}/recall` or `/recall`
  as appropriate.
- Fixed `confidence_score` → `confidence` in SPEC-v0.5.md.
- Clarified audit check names vs review_reasons types in README.
- Updated `.env.example` with missing env vars
  (event webhook URL, auth config, rate limit vars).
- Updated docker-compose image tag to `0.5`.

## v0.4.0 (2026-05-16)

### Security
- **Write access enforcement**: All mutating endpoints (`/draft`, `/v1/facts` POST/DELETE,
  `/vault/{name}/draft`) now enforce write claims from auth context.
  A read-only API key can no longer write to the vault. (#118)
- **Vault scope enforcement**: `withVault` middleware now checks
  `Claims.HasVaultAccess(name)`. A token scoped to vault `docs` can no longer
  access `/vault/finance/*`. (#119)

### Bug Fixes
- **`/vaults` stats per vault**: Replaced `indexerFor()` with `ForEach()` loop.
  Each vault now reports its own file/chunk counts instead of all showing the
  first vault's values. (#112)
- **`/vaults` single-tenant stats**: Single-tenant mode now returns real
  indexed_files and total_chunks instead of hardcoded 0. (#126)
- **`/draft` vault path in multi-tenant**: Resolves vault-specific path from
  `cfg.Vaults[name].Path` instead of `cfg.VaultPath` (which is empty in
  multi-tenant mode). (#113, #121)
- **`/audit` vault path in multi-tenant**: `checkStaleness`, `checkGaps`,
  `checkDuplicates` now accept a vault root path parameter instead of using
  `s.cfg.VaultPath` directly. (#121)
- **`/stats` Qdrant client in multi-tenant**: Uses per-vault Qdrant client
  via `indexers.GetClient(vaultName)` instead of hardwired `s.qdrant.Count()`. (#128)
- **`entityGraph` BFS rescroll**: Pre-loads the full source_file → links mapping
  once before the BFS loop instead of re-scrolling the entire collection from
  `nil` offset on every hop. (#114)
- **`entityGraph` entity search**: Searches vault collection (with `source_file`
  payload) instead of facts collection (key-value facts with no file data). (#115)
- **`displayName`**: Uses `filepath.Ext` + `strings.TrimSuffix` instead of
  `strings.LastIndex(path, ".")` — fixes filenames with dots in directory names. (#127)
- **`/reindex` rate limit bucket**: Uses dedicated `RateLimitReindex` field
  instead of `/recall` bucket. Added `RAGAMUFFIN_RATE_LIMIT_REINDEX` env var. (#117)

### Infrastructure
- **Signal handler consolidation**: Single- and multi-tenant paths now use one
  coordinated signal handler that sequences: cancel indexers → close watchers →
  shutdown HTTP server. No more racing goroutines. (#116)
- **`go.mod`**: Changed `go 1.25.0` to `go 1.25` (no patch version in go directive). (#124)
- **`EventWebhookURL` alignment**: Fixed tab/space indent in config struct init. (#125)
- **NoneAuthenticator nil guard**: Added nil check for `*http.Request` in
  `NoneAuthenticator.Authenticate()` so future implementations following the
  same pattern don't panic. (#120)

### Documentation
- **Global routes documented**: Added note to README that `/v1/facts`,
  `/v1/logs`, `/v1/snapshot` are instance-wide in multi-tenant mode, not
  per-vault. (#122)

### Enhancement
- **Web UI 404 for non-SPA paths**: Added `Accept: application/json` check
  to the static catch-all. API tooling now gets proper JSON 404s. (#123)
