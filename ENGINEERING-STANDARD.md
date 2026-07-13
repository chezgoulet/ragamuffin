# Engineering Standard

The bar for every Ragamuffin contribution, frontend and backend. No deferred
fixes, no "I'll do it later." If a change ships, it meets this standard.

## Frontend — Error Recovery

All network I/O goes through the `apiFetch` / `apiJSON` wrapper in
`web/static/app.js`. Never call `fetch()` directly from a view.

The wrapper provides:

- **Retry with exponential backoff** — 3 attempts, `1s → 2s → 4s`.
- **Per-attempt timeout** — 10 seconds via `AbortController`.
- **Circuit breaker** — after 3 consecutive failures on the same logical
  endpoint, calls fail fast until the endpoint recovers.
- **No retry on 4xx** — client errors surface immediately.
- **Offline detection** — `navigator.onLine` events plus a 30s `/health`
  probe. The `#offline-banner` shows when the connection is lost.

## Frontend — State Management

Every view that fetches data must render three states via the shared helpers:

| State | Helper | When |
|---|---|---|
| Loading | `renderLoading(container, label)` | While a fetch is in flight |
| Empty | `renderEmpty(container, message, hint)` | Fetch succeeded, no data |
| Error | `renderError(container, message, onRetry)` | Fetch failed; includes Retry button |

Transitions must not flicker — render loading before the fetch, replace on
resolution.

## Frontend — Accessibility (WCAG AA)

- ARIA labels on all interactive elements (buttons, inputs, nav, graph nodes).
- Tabs use `role="tablist"` / `role="tab"` / `aria-selected`; arrow keys move
  between tabs, Enter/Space activate.
- Keyboard: Tab order is logical, Enter/Space activate, Escape closes dialogs.
- Focus management: focus returns to the triggering element after a modal
  closes.
- Visible focus ring (`:focus-visible`) meeting AA contrast.
- Honor `prefers-contrast: more`.

## Backend

- Every new endpoint is documented in `SPEC.md` **before** implementation.
- Handlers follow decode → validate → execute → respond.
- Error responses use `writeError(w, status, code, message)`; success uses
  `writeJSON(w, status, data)`.
- Every new handler ships with unit tests.
- Every new route gets a curl entry in `smoke_test.sh`.
- No external frontend dependencies. No new Go dependencies beyond the
  allowed list (Qdrant gRPC client, `modernc.org/sqlite`).

## Verification

- Retry/offline behavior is testable: kill the Ragamuffin process and observe
  graceful degradation (banner appears, retry buttons work on recovery).
- `go build ./...`, `go test ./internal/... -short`, `go vet ./...` are green
  (CI enforces).
