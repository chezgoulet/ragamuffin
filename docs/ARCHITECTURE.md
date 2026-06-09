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

- `go build ./...` — compiles cleanly
- `go test -short ./internal/...` — unit tests pass
- `go vet ./...` — no static analysis issues

This check **must pass** before the PR can merge. It's designed to be fast
(< 2 min) — no external dependencies, no Docker, no benchmark runs.

### Tier 2 — `testing` (Staging)

The `testing` branch is the integration point. Every merge triggers
`testing-push.yml`:

- Builds the `:rolling` Docker image
- Pushes to container registry
- Deploys to staging for validation

This is where changes bake before reaching production. Agents can pull
`:rolling` to preview what's coming. If a bug is found here, it's fixed
before it reaches `main`.

### Tier 3 — `main` (Production)

Release PRs from `testing` → `main` trigger `build.yml`:

- Full benchmark gauntlet (all configs, both datasets)
- Compares results against `benchmarks/baseline.json`
- If benchmarks pass: builds `:latest` image, cuts a git tag
- If benchmarks regress: PR is flagged for review

Only `testing` → `main` PRs land here. No direct commits.

## Workflow Files

| File | Trigger | What It Does |
|---|---|---|
| `.github/workflows/pr-check.yml` | PR to `testing` | `go build`, `go test -short`, `go vet` |
| `.github/workflows/testing-push.yml` | Push to `testing` | Build `:rolling`, smoke tests |
| `.github/workflows/build.yml` | Push to `main` | Benchmark gauntlet, `:latest`, git tag |

## Tag Semantics

| Tag | Source | Updated By | Stability |
|---|---|---|---|
| `chezgoulet/ragamuffin:rolling` | `testing` | Every testing push | Pre-release |
| `chezgoulet/ragamuffin:latest` | `main` | Every main push | Production |
| `vX.Y.Z` (git tag) | `main` | Every main push | Release |

The `:rolling` tag follows `testing` tip without versioning. Version tags
are applied to `main` commits only.

## Benchmark Gauntlet

The full benchmark suite runs on every merge to `main`:

```
testing:merge ──→ build.yml ──→ benchmark gauntlet ──→ pass → promote → :latest
                                                     └─→ fail → flag PR
```

### What It Tests

- Both datasets: LongMemEval (~500 questions) and LoCoMo (~1,986 pairs)
- All four configurations (A, B, C, D)
- Results compared against `benchmarks/baseline.json`

### Baseline Management

`benchmarks/baseline.json` stores the reference accuracy per question type.
It's updated only at release time (when `main` is tagged). The gauntlet
compares new results against this baseline; significant regressions block
promotion.

## Auto-Revert Mechanism

If a `testing` → `main` merge causes benchmark regressions:

1. The `build.yml` workflow reports the failure
2. The release PR is marked as failing — does not produce `:latest`
3. The commit remains on `main` but `:latest` is not updated
4. A fix is developed on a new `dev/*` branch, goes through the normal
   testing pipeline, and a new release PR addresses the regression

There is no automatic revert — human judgment is required to assess whether
the regression is acceptable (e.g., tradeoff for a feature improvement).

## When This Pattern Applies

This three-tier pipeline is suitable for repos that:

- Have a release-quality `main` branch with benchmarked performance
- Need a staging step between dev and production (e.g., Docker images)
- Have CI that takes > 5 minutes per run (so per-PR checks are minimal,
  full validation happens at merge time)

For smaller repos or pure libraries without Docker deployment, a simpler
two-tier model (dev → main) is sufficient. The full three-tier model is
recommended for any repo that produces deployable artifacts.

## Cross-Repo Reference

Other ChezGoulet repos using this pattern:

- **chezgoulet/infra** — infrastructure-as-code, uses testing/main with
  `:rolling`/`:latest` for Docker images
- **chezgoulet/library** — knowledge base, uses main-only (no artifacts)

When adopting this pattern, create the three workflow files and configure
branch protection rules:

- `testing`: require `pr-check.yml` to pass, require PR review
- `main`: require `build.yml` to pass, require PR from `testing`
