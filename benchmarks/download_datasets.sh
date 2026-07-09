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
echo "=== Downloading NarrativeQA (DeepMind) via HF dataset-viewer ==="
# NarrativeQA ships as parquet files. The benchmark loader downloads them
# automatically on first run; this step is an explicit opt-in.
if [ -d "$DATA_DIR/NarrativeQA/parquet/default" ]; then
    echo "Already present at $DATA_DIR/NarrativeQA — skipping"
else
    echo "Triggering NarrativeQA parquet download via the loader..."
    echo "(requires pyarrow: pip install -r $BENCH_DIR/requirements.txt)"
    (cd "$BENCH_DIR" && python3 -c "from benchmarks.loaders.narrativeqa import NarrativeQALoader; NarrativeQALoader('$DATA_DIR/NarrativeQA').download()")
fi

echo ""
echo "=== Done ==="
echo "LongMemEval: $DATA_DIR/LongMemEval"
echo "LoCoMo:      $DATA_DIR/Backboard-Locomo-Benchmark"
echo "NarrativeQA: $DATA_DIR/NarrativeQA"
