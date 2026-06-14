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
echo ""
echo "=== NarrativeQA (HuggingFace parquet) ==="
# Downloaded by ingest_narrativeqa.py on first run
NQA_DIR="$DATA_DIR/narrativeqa"
mkdir -p "$NQA_DIR"
if [ -d "$NQA_DIR/stories" ] && [ -f "$NQA_DIR/questions.json" ]; then
    echo "Already parsed: $(ls "$NQA_DIR/stories" 2>/dev/null | wc -l) stories, $(cat "$NQA_DIR/questions.json" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo "?") questions"
else
    echo "Run 'python3 benchmarks/ingest_narrativeqa.py' to download and parse."
fi

echo ""
echo "=== Done ==="
echo "LongMemEval:   $DATA_DIR/LongMemEval"
echo "LoCoMo:        $DATA_DIR/Backboard-Locomo-Benchmark"
echo "NarrativeQA:   $DATA_DIR/narrativeqa
