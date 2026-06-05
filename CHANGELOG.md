# Changelog

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
