#!/usr/bin/env bash
# Full benchmark sweep — all configs, both datasets.
# Usage: ./run_all.sh [--quick]
#   --quick: runs one config instead of all four (for smoke testing)
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
RESULTS="$BENCH_DIR/RESULTS.md"

CONFIGS=(a b c d)
if [ "${1:-}" = "--quick" ]; then
    CONFIGS=(a)
    echo "Quick mode — Config A only"
fi

python3 -c "import requests" 2>/dev/null || { echo "Install requests: pip install -r $BENCH_DIR/requirements.txt"; exit 1; }

echo "# Benchmark Results" > "$RESULTS"
echo "" >> "$RESULTS"
echo "Run: $(date -u '+%Y-%m-%d %H:%M UTC')" >> "$RESULTS"
echo "" >> "$RESULTS"
echo "## Summary" >> "$RESULTS"
echo "" >> "$RESULTS"

for BMARK in longmemeval locomo; do
    echo "--- $BMARK ---"
    for CFG in "${CONFIGS[@]}"; do
        echo "  Config $CFG..."
        python3 "$BENCH_DIR/run.py" --benchmark "$BMARK" --config "$CFG" 2>&1 | tee -a "$RESULTS"
        echo "" >> "$RESULTS"
    done
done

echo "Done. Results in $RESULTS"
