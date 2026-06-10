# CI Pipeline Architecture

Ragamuffin uses a **three-tier staged-branch CI pipeline** designed to catch
regressions early and keep `main` production-safe. This document describes
the pipeline for cross-repo reference within the ChezGoulet House.

## Branch Topology

```
dev/*  ──(PR)──→  testing  ──(PR)──→  main
```

### Tier 1 — Feature Branches (`dev/*`)

All development happens on branches from `testing`. Every PR triggers
`pr-check.yml`:

- `gofmt -l .` — all files formatted (§gofmt)
- `go build ./...` — compiles cleanly
- `go test ./internal/... ./cmd/...` — unit tests pass
- `go vet ./internal/... ./cmd/...` — no static analysis issues
- `govulncheck` — no known vulnerable dependencies

This check **must pass** before the PR can merge. It's designed to be fast
(< 2 min) — no external dependencies, no Docker, no benchmark runs.

### Tier 2 — `testing` (Staging)

The `testing` branch is the integration point. Every merge triggers
`testing-push.yml`:

- `gofmt` + `go vet` — quality gates
- `go test ./internal/... ./cmd/...` — all tests pass
- Builds the `:rolling` Docker image
- Pushes to container registry
- Deploys to staging for validation (manual)

This is where changes bake before reaching production. Agents can pull
`:rolling` to preview what's coming. If a bug is found here, it's fixed
before it reaches `main`.

### Tier 3 — `main` (Production)

Tagged releases (git tag `v*`) trigger `build.yml`:

- `gofmt` + `go vet` + `govulncheck` — quality gates
- `go test ./internal/... ./cmd/...` — all tests pass
- Docker build + push (`:latest` + version tag)
- Cross-compile release binaries (linux + darwin, amd64 + arm64)
- GitHub Release with binaries and release notes

Only `testing` → `main` PRs land here. No direct commits.

## Workflow Files

| File | Trigger | What It Does |
|---|---|---|
| `.github/workflows/pr-check.yml` | PR to `testing` | `gofmt`, `go vet`, `govulncheck`, tests, Docker verify |
| `.github/workflows/testing-push.yml` | Push to `testing` | Quality gates + `:rolling` Docker image |
| `.github/workflows/build.yml` | Tag push `v*` | Quality gates, tests, Docker + binary release |

## Tag Semantics

| Tag | Source | Updated By | Stability |
|---|---|---|---|
| `chezgoulet/ragamuffin:rolling` | `testing` | Every testing push | Pre-release |
| `chezgoulet/ragamuffin:latest` | `main` | Every tagged release | Production |
| `vX.Y.Z` (git tag) | `main` | Every tagged release | Release |

The `:rolling` tag follows `testing` tip without versioning. Version tags
are applied to `main` commits only.

## Benchmark Gauntlet

A benchmark suite (`benchmarks/`) exists for LongMemEval and LoCoMo datasets
but is currently **disabled in CI** (`benchmark-gauntlet.yml.disabled`). It
requires Qdrant and an embedding provider. To re-enable:

1. Set up a CI-compatible Qdrant service
2. Configure an embedding provider (e.g., OpenAI-compatible)
3. Rename `benchmark-gauntlet.yml.disabled` → `benchmark-gauntlet.yml`
4. Wire it into the release workflow as a gating step

See `benchmarks/README.md` for local usage.

## Cross-Repo Reference

Other ChezGoulet repos using this pattern:

- **chezgoulet/infra** — infrastructure-as-code, uses testing/main with
  `:rolling`/`:latest` for Docker images
- **chezgoulet/library** — knowledge base, uses main-only (no artifacts)

When adopting this pattern, create the three workflow files and configure
branch protection rules:

- `testing`: require `pr-check.yml` to pass, require PR review
- `main`: require `build.yml` to pass, require PR from `testing`
