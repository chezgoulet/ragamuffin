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
echo "=== Downloading NarrativeQA ==="
NQA_DIR="$DATA_DIR/narrativeqa"
mkdir -p "$NQA_DIR/train" "$NQA_DIR/test" "$NQA_DIR/validation"

# Train: 24 shards
echo "Downloading train shards (24 files)..."
for i in $(seq -w 0 23); do
    F="train-000$i-of-00024.parquet"
    if [ -f "$NQA_DIR/train/$F" ] && [ -s "$NQA_DIR/train/$F" ]; then
        echo "  $F — cached"
    else
        echo -n "  $F — downloading... "
        curl -sL "https://huggingface.co/datasets/deepmind/narrativeqa/resolve/main/data/$F?download=1" -o "$NQA_DIR/train/$F"
        echo "$(du -h "$NQA_DIR/train/$F" | cut -f1)"
    fi
done

# Test: 8 shards
echo "Downloading test shards (8 files)..."
for i in $(seq -w 0 7); do
    F="test-0000$i-of-00008.parquet"
    if [ -f "$NQA_DIR/test/$F" ] && [ -s "$NQA_DIR/test/$F" ]; then
        echo "  $F — cached"
    else
        echo -n "  $F — downloading... "
        curl -sL "https://huggingface.co/datasets/deepmind/narrativeqa/resolve/main/data/$F?download=1" -o "$NQA_DIR/test/$F"
        echo "$(du -h "$NQA_DIR/test/$F" | cut -f1)"
    fi
done

# Validation: 3 shards
echo "Downloading validation shards (3 files)..."
for i in $(seq -w 0 2); do
    F="validation-0000$i-of-00003.parquet"
    if [ -f "$NQA_DIR/validation/$F" ] && [ -s "$NQA_DIR/validation/$F" ]; then
        echo "  $F — cached"
    else
        echo -n "  $F — downloading... "
        curl -sL "https://huggingface.co/datasets/deepmind/narrativeqa/resolve/main/data/$F?download=1" -o "$NQA_DIR/validation/$F"
        echo "$(du -h "$NQA_DIR/validation/$F" | cut -f1)"
    fi
done

echo ""
echo "=== Done ==="
echo "LongMemEval: $DATA_DIR/LongMemEval"
echo "LoCoMo:      $DATA_DIR/Backboard-Locomo-Benchmark"
echo "NarrativeQA: $NQA_DIR/ ({train,test,validation}/)
