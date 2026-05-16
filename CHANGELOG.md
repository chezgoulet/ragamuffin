# Changelog

## v0.4-postreview (unreleased)

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
