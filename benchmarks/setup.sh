#!/usr/bin/env bash
# Benchmark setup: downloads datasets, builds image, starts Qdrant + Ragamuffin,
# runs all benchmarks, tears down.
#
# Usage:
#   export RAGAMUFFIN_EMBEDDING_API_KEY=sk-...
#   ./benchmarks/setup.sh            # full sweep
#   ./benchmarks/setup.sh --quick    # single config (smoke test)

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$BENCH_DIR/.." && pwd)"
COMPOSE_FILE="$REPO_DIR/docker-compose.yml"
COMPOSE_OVERRIDE="$BENCH_DIR/docker-compose.benchmark.yml"
RESULTS_DIR="$BENCH_DIR/results"
QUICK=""
[[ "${1:-}" = "--quick" ]] && QUICK="--quick"

if [ -z "${RAGAMUFFIN_EMBEDDING_API_KEY:-}" ]; then
  echo "ERROR: RAGAMUFFIN_EMBEDDING_API_KEY is not set."
  exit 1
fi

# ── Dependencies ────────────────────────────────────────────────────────────
echo "=== Installing Python deps ==="
pip3 install -q -r "$BENCH_DIR/requirements.txt" 2>/dev/null || true

echo "=== Downloading datasets ==="
bash "$BENCH_DIR/download_datasets.sh"

# ── Build image ─────────────────────────────────────────────────────────────
echo "=== Building ragamuffin:benchmark ==="
docker build -t ragamuffin:benchmark -f "$REPO_DIR/Dockerfile" "$REPO_DIR"

# ── Start stack ─────────────────────────────────────────────────────────────
echo "=== Starting Qdrant + Ragamuffin ==="
export RAGAMUFFIN_EMBEDDING_API_KEY
export RAGAMUFFIN_LOG_LEVEL=error
docker compose -f "$COMPOSE_FILE" -f "$COMPOSE_OVERRIDE" up -d --wait

# ── Wait for readiness ──────────────────────────────────────────────────────
echo "=== Waiting for Ragamuffin ==="
RAGAMUFFIN_URL="${RAGAMUFFIN_URL:-http://localhost:8000}"
for i in $(seq 1 30); do
  if curl -sf "$RAGAMUFFIN_URL/health" >/dev/null 2>&1; then
    echo "  ready after ${i}s"; break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: did not start"
    docker compose -f "$COMPOSE_FILE" logs ragamuffin --tail 20
    exit 1
  fi
  sleep 2
done

# ── Run benchmarks ──────────────────────────────────────────────────────────
echo "=== Running benchmarks ==="
mkdir -p "$RESULTS_DIR"
export RAGAMUFFIN_URL
export QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"
cd "$BENCH_DIR" && bash run_all.sh $QUICK

# ── Save results ────────────────────────────────────────────────────────────
cp "$BENCH_DIR/RESULTS.md" "$RESULTS_DIR/results-$(date -u '+%Y%m%d-%H%M%S').md"
echo "=== Results ==="
head -30 "$BENCH_DIR/RESULTS.md"
echo "Full results: $RESULTS_DIR"

# ── Cleanup ─────────────────────────────────────────────────────────────────
echo "=== Cleaning up ==="
docker compose -f "$COMPOSE_FILE" -f "$COMPOSE_OVERRIDE" down --volumes
