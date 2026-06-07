#!/usr/bin/env python3
"""
Split the LongMemEval 277MB JSON array into individual conversation files.

Reads benchmarks/data/LongMemEval/data/longmemeval_s_cleaned.json
(a single JSON array of conversations) and writes one file per
conversation to benchmarks/data/LongMemEval/data/S/<id>.json.

This is a one-time setup step so the existing load_longmemeval()
loader can load per-conversation files without OOM.

Usage:
    python3 benchmarks/split_longmemeval.py

Requires:
    pip install ijson   # streaming JSON parser (low memory)
"""

import json
import os
import sys

try:
    import ijson
except ImportError:
    print("ERROR: ijson required.  pip install ijson")
    sys.exit(1)


# ── Paths ───────────────────────────────────────────────────────────────────
HERE = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(HERE, "data", "LongMemEval", "data")
BLOB_PATH = os.path.join(DATA_DIR, "longmemeval_s_cleaned.json")
OUT_DIR = os.path.join(DATA_DIR, "S")


def get_conversation_id(conv: dict) -> str:
    """Extract a stable identifier from the conversation object."""
    for key in ("conversation_id", "id", "session_id", "idx", "index"):
        val = conv.get(key)
        if val is not None:
            return str(val)
    # Fallback: hash of first turn content
    turns = conv.get("turns", conv.get("history", conv.get("conversation", [])))
    if turns and isinstance(turns, list) and len(turns) > 0:
        first = turns[0]
        if isinstance(first, dict):
            content = first.get("content", first.get("text", str(first)))
        else:
            content = str(first)
        return f"conv_{abs(hash(content)) % 10**8}"
    return "conv_unknown"


def main():
    if not os.path.isfile(BLOB_PATH):
        print(f"Blob not found: {BLOB_PATH}")
        print("Have you downloaded the LongMemEval dataset?")
        sys.exit(1)

    os.makedirs(OUT_DIR, exist_ok=True)

    count = 0
    existing = 0
    errors = 0
    print(f"Reading: {BLOB_PATH}")
    print(f"Writing to: {OUT_DIR}")

    with open(BLOB_PATH, "rb") as fh:
        # ijson.items streams complete objects from the top-level array
        for conversation in ijson.items(fh, "item"):
            try:
                conv_id = get_conversation_id(conversation)
                out_path = os.path.join(OUT_DIR, f"{conv_id}.json")

                if os.path.exists(out_path):
                    existing += 1
                    continue

                with open(out_path, "w") as outf:
                    json.dump(conversation, outf, indent=2)

                count += 1
                if count % 50 == 0:
                    print(f"  ... {count} conversations split")

            except Exception as e:
                errors += 1
                print(f"  WARN: skipped conversation at index {count + existing + errors}: {e}")

    print(f"\nDone: {count} new files, {existing} already existed, {errors} skipped")


if __name__ == "__main__":
    main()
