#!/usr/bin/env bash
# Downloads benchmark datasets into benchmarks/data/
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="$BENCH_DIR/data"
mkdir -p "$DATA_DIR"

echo "=== Downloading LongMemEval ==="
if [ -d "$DATA_DIR/LongMemEval" ]; then
    echo "Already exists, pulling latest..."
    cd "$DATA_DIR/LongMemEval" && git pull
else
    cd "$DATA_DIR"
    git clone https://github.com/xiaowu0162/LongMemEval.git
fi

echo ""
echo "=== Downloading LoCoMo (Backboard) ==="
if [ -d "$DATA_DIR/Backboard-Locomo-Benchmark" ]; then
    echo "Already exists, pulling latest..."
    cd "$DATA_DIR/Backboard-Locomo-Benchmark" && git pull
else
    cd "$DATA_DIR"
    git clone https://github.com/Backboard-io/Backboard-Locomo-Benchmark.git
fi

echo ""
echo "=== Done ==="
echo "LongMemEval: $DATA_DIR/LongMemEval"
echo "LoCoMo:      $DATA_DIR/Backboard-Locomo-Benchmark"
