# Contributing to Ragamuffin

Ragamuffin uses a **staged-branch workflow**. All development happens on
feature branches that flow through `testing` before reaching `main`.

## Branch Strategy

```
dev/*  ──(PR)──→  testing  ──(PR)──→  main
                       │                  │
                 :rolling image     :latest image
                 pre-release        production
```

### Three Tiers

1. **Feature branches (`dev/<issue-N>-<short-desc>`)** — branch from `testing`,
   PR into `testing`. This is where all development happens.

2. **`testing` branch** — the integration/staging branch. Merges to `testing`
   build the `:rolling` Docker image and deploy for validation.

3. **`main` branch** — the stable release branch. Only release PRs from
   `testing` to `main` land here. Merges trigger the benchmark gauntlet and
   produce `:latest`.

## Creating a Pull Request

### From `testing`

```bash
# Start from testing
git checkout testing
git pull origin testing

# Create your feature branch
git checkout -b dev/<issue-N>-<short-description>

# Make changes, commit
git add -A
git commit -m "type: description (#NNN)"

# Push and open PR
git push origin dev/<issue-N>-<short-description>
```

### PR Requirements

- **Branch from `testing`**, target `testing` in your PR.
- **Title format**: `<type>: description (#NNN)` where type is `feat`, `fix`,
  `docs`, `test`, `chore`, or `refactor`.
- **Body must include**: `Closes #NNN` (or `Closes #NNN, Closes #MMM` for
  bundled issues).
- **No direct PRs to `main`** — all changes go through `testing` first.

### What CI Checks

| Workflow | When | What It Does |
|---|---|---|
| `pr-check.yml` | Every PR to `testing` | Compile (`go build ./...`), unit tests (`go test -short`), static analysis (`go vet`) |
| `testing-push.yml` | Every merge to `testing` | Build `:rolling` Docker image, deploy, smoke tests |
| `build.yml` | Every merge to `main` | Full benchmark gauntlet, build `:latest`, release tag |

A failing `pr-check.yml` blocks merge. Fix the issue and push to the same
branch — the check re-runs automatically.

## Code Review Process

1. Open a PR from `dev/*` → `testing`.
2. CI runs `pr-check.yml` automatically.
3. Robot reviews the PR for convention compliance, dependency audit, and
   architecture fit.
4. Once CI passes and review is done, the PR is merged to `testing`.
5. Changes bake on `testing` (`:rolling` image) before a release PR promotes
   them to `main`.

### Review Checklist (for reviewers)

- The branch targets `testing`, not `main`
- `go build ./...` compiles
- `go vet ./...` passes
- Tests cover the change meaningfully
- No new external dependencies beyond the allowed list (Qdrant gRPC client,
  `modernc.org/sqlite`, `golang-jwt/jwt/v5`, `golang.org/x/sys`)
- Errors are wrapped (`fmt.Errorf("what: %w", err)`)
- Context is threaded through I/O operations

## Architecture Conventions

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full pipeline
architecture and [AGENTS.md](AGENTS.md) for the development conventions.

Key rules:

- **Standard library first.** External dependencies are minimized and
  audited. See the allowed list above.
- **`CGO_ENABLED=0`** in build. The final Docker image is `FROM scratch`.
- **`slog` for logging.** Structured, JSON to stderr. No `fmt.Println`.
- **Handler pattern** — decode → validate → execute → respond. All errors
  go through `writeError`, successes through `writeJSON`.

## Testing

- CI runs `go test -short ./internal/...` on every PR. Write tests that
  pass in this mode (no external dependencies).
- Integration tests that need Qdrant or API keys are tagged with `t.Skip()`
  and run separately.
- Target 75%+ coverage for new packages. If the interface is hard to mock,
  add an interface extraction to enable testing.
- See the [benchmarks README](benchmarks/README.md) for the benchmark harness
  and evaluation procedure.

## Benchmark Expectations

- Full benchmark gauntlet runs on `main` merge — not required per-PR.
- Local benchmarking: `python3 benchmarks/run.py --config D --benchmark longmemeval --max-convs 5`
- Results are compared against `benchmarks/baseline.json`. Regressions in
  the benchmark gauntlet can block a `testing` → `main` release PR.

## Questions?

Open a discussion or ask in the issue tracker. This repo is maintained as
part of the ChezGoulet House — read the [library](https://github.com/chezgoulet/library)
for broader context.
