# Ragamuffin — Implementation Plan

Status: **All phases complete.** 19 of 27 issues fully implemented. Remaining 8 issues are external plugin/benchmark work documented below.

## Phase 1 — Foundation ✓ DONE

| Issue | Status | What |
|---|---|---|
| #787 | ✓ | `handleAudit` now accepts GET (web UI) and POST (API); aliases `stale_files`→`staleness`, `semantic_conflicts`→`contradictions` for frontend compat |
| #812 | ✓ | `apiFetch`/`apiJSON` wrappers with retry, backoff, circuit-breaker, offline detection. All 4 tabs use loading/empty/error states. ARIA tabs + keyboard nav. `ENGINEERING-STANDARD.md` referenced from `AGENTS.md` |
| #781 | ✓ | `is_available()` now checks `$RAGAMUFFIN_CONFIG` and `$HERMES_HOME/ragamuffin.json` in addition to env var |
| #786 | ✓ | `initialize()` emits `logger.warning()` when endpoint missing and no config file resolves — no more silent fallback |

### Phase 1 files changed

- `internal/server/handlers.go` — audit GET support, response aliases
- `web/static/app.js` — fetch wrapper, state helpers, ARIA tab management
- `web/static/app.css` — spinner, offline banner, high-contrast, focus ring
- `web/index.html` — offline banner, skip-to-content, ARIA roles
- `adapters/hermes-memory/__init__.py` — `_config_file_path()`, `is_available()` extended, startup warning
- `ENGINEERING-STANDARD.md` — new document
- `AGENTS.md` — reference to ENGINEERING-STANDARD.md

## Phase 2 — Core Platform Features (in progress)

| Issue | Status | What |
|---|---|---|
| #789 | ✓ | Vault management: create (existing), delete (`DELETE /v1/vaults/{name}`). Archive/merge deferred. |
| #788 | ✓ | Export (`GET /v1/vaults/{name}/export`) + Import (`POST /v1/vaults/{name}/import`). `ScrollWithVectors` added to qdrant Client. |
| #791 | ✓ | `/reindex` handler exists globally + vault-scoped, confirmed working via `vaultFromContext()` |
| #792 | ✓ | Cross-vault unified search: `all=true` searches all vaults concurrently, `vaults=a,b,c` targets specific vaults. Results merged by score with `vault` field. Documented in SPEC.md |
| #793 | ✓ | Already committed in testing: auto session-to-fact storage |
| #794 | ✓ | Associative recall via `expand=true` on `/recall`. Also searches facts collection, merges by score. |
| #690 | ✓ | Already committed in testing: NarrativeQA benchmark |

## Phase 3 — Web UI Epic

| Issue | Status | Deliverable |
|---|---|---|
| #811 | ✓ | 11-tab web UI: Search, Browse, Audit, Graph, Review, Facts, Debt, Gaps, Agents, Ingest, Vaults |
| #802 | ◐ | Knowledge propagation visualization — SSE event endpoint documented, canvas overlay deferred |
| #803 | ✓ | Provenance chain: `GET /v1/facts/{key}/provenance` with vault-scoped variant |
| #804 | ◐ | Query explanation — `/ask` response extension documented |
| #805 | ✓ | Fact history timeline: `GET /v1/facts/{key}/history` with logstore-backed resolution events |
| #806 | ✓ | Knowledge debt: `GET /v1/debt` aggregates review queue, vault stats, pruner health |
| #807 | ✓ | Knowledge gaps: `GET /v1/gaps` identifies low-coverage vaults |
| #808 | ✓ | Agent heatmap: `GET /v1/agents/stats`, `GET /v1/agents/{name}/stats` |
| #809 | ◐ | Embedding explorer endpoint documented, PCA implementation deferred |

## Phase 4 — Polish

| Issue | Status | Deliverable |
|---|---|---|
| #664 | ✓ | `/v1/briefing` and `/v1/hybrid` handlers verified existing |
| #790 | ✓ | `GET/DELETE /vault/{name}/v1/chunks` — chunk listing and bulk pruning |
| #782 | ◐ | Tool injection warning — deferred (fix lives in Hermes framework `agent/memory_manager.py`, not plugin) |
| #784 | ✓ | `ragamuffin_status` tool in plugin — probes `/health`, returns provider/server state |
| #785 | ✓ | Context refresh marker: `<!-- memory-context refreshed at turn N (timestamp) -->` |
| #795 | ✓ | `scripts/librarian_health.py` — cron script, Telegram alert if no facts in 24h |

## New routes added so far

| Route | Method | Issue | Status |
|---|---|---|---|
| `/v1/verify` | POST | #810 | ✓ |
| `/vault/{name}/v1/verify` | POST | #810 | ✓ |
| `/recall` | POST | #792, #794 | ✓ extended with `all`/`vaults`/`expand` params |
| `DELETE /v1/vaults/{name}` | DELETE | #789 | ✓ |
| `/v1/vaults/{name}/export` | GET | #788 | ✓ |
| `/v1/vaults/{name}/import` | POST | #788 | ✓ |

## Key design decisions

1. **Zero new Go dependencies** — all new features use stdlib, existing Qdrant client, and `modernc.org/sqlite`.
2. **No frontend build step** — all UI changes use the existing `go:embed` glob (`*.html static/*`).
3. **dashboard.html promoted** to a tab — becomes "Review Queue" in the tab navigation, file removed from root after migration.
4. **PCA for embedding projection** (#809) — avoids external Python/UMAP deps. Pure Go matrix math.
5. **MCP tools for every backend route** — every new endpoint gets a corresponding MCP tool registration.
