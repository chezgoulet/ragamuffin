# Ragamuffin — Implementation Plan

Status: **Phase 1 complete, Phase 2 in progress.**

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
| #810 | ✓ | `/v1/verify` handler + `doVerify` + MCP `ragamuffin_verify` tool. Accepts a fact, searches vault, groups supporting/conflicting sources, optional LLM conflict summary. Documented in SPEC.md |
| #789 | ◐ | Vault management: create, merge, archive, delete — **pending** |
| #788 | ◐ | Export/import vault data — **pending** |
| #791 | ◐ | Manual re-index trigger — `/reindex` exists but needs vault-scoped variant verified |
| #792 | ◐ | Cross-vault unified search — **pending** |
| #794 | ◐ | Associative recall / query expansion — **pending** |
| #793 | ✓ | Already committed in testing: auto session-to-fact storage |
| #690 | ✓ | Already committed in testing: NarrativeQA benchmark |

## Phase 3 — Web UI Epic

All issues in this phase are **pending** implementation. See PLAN.md plan section for
the full tab architecture.

| Tab | Issues | Backend routes | Frontend estimate |
|---|---|---|---|
| Search (enhanced) | #804 | Extend `/ask` with `explanation` field | ~100 lines |
| Facts Manager | #803, #805 | `/v1/facts/{key}/provenance`, `/v1/facts/{key}/history` | ~450 lines |
| Review Queue | #811 | `/v1/review/stats`, `/v1/review` | Promoted from dashboard.html |
| Ingest Log | #811 | `/v1/logs` | ~150 lines |
| Pruner Config | #811 | `/v1/pruner/config` | ~100 lines |
| Vault Admin | #811, #789, #788, #791 | vault CRUD, export | ~300 lines |
| Knowledge Debt | #806 | `/v1/debt` | ~200 lines |
| Knowledge Gaps | #807 | `/v1/gaps` | ~200 lines |
| Agent Heatmap | #808 | `/v1/agents/stats` | ~250 lines |
| Embedding Explorer | #809 | `/v1/embedding/project` | ~400 lines |
| Graph (enhanced) | #802 | `/events/query` SSE | ~300 lines |

## Phase 4 — Polish & External

| Issue | Status | Notes |
|---|---|---|
| #664 | ◐ | UX review: `/v1/briefing` route exists but needs verification; `/v1/hybrid` exists |
| #782 | ◐ | Plugin tool injection warning — external Hermes plugin |
| #784 | ◐ | Plugin health introspection tool — external Hermes plugin |
| #785 | ◐ | Plugin prefetch timing signal — external Hermes plugin |
| #790 | ◐ | Chunk-level inspection/pruning endpoints |
| #795 | ◐ | Librarian health check — external script |

## New routes added so far

| Route | Method | Issue | Status |
|---|---|---|---|
| `/v1/verify` | POST | #810 | ✓ |
| `/vault/{name}/v1/verify` | POST | #810 | ✓ |

## Key design decisions

1. **Zero new Go dependencies** — all new features use stdlib, existing Qdrant client, and `modernc.org/sqlite`.
2. **No frontend build step** — all UI changes use the existing `go:embed` glob (`*.html static/*`).
3. **dashboard.html promoted** to a tab — becomes "Review Queue" in the tab navigation, file removed from root after migration.
4. **PCA for embedding projection** (#809) — avoids external Python/UMAP deps. Pure Go matrix math.
5. **MCP tools for every backend route** — every new endpoint gets a corresponding MCP tool registration.
