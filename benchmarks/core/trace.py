"""Trace recording and export.

Writes one JSON line per Result to a .jsonl file.
Supports reading traces back for analysis or re-classification.
"""

from __future__ import annotations

import json
import logging
import os
from typing import Dict, List, Optional

from .types import Result

logger = logging.getLogger("ragamuffin.benchmark")

TRACE_DIR = os.environ.get("RAGAMUFFIN_TRACE_DIR", ".benchmark_traces")


class TraceWriter:
    """Writes benchmark results to a JSONL trace file."""

    def __init__(self, path: str):
        self.path = path
        self._file: Optional[object] = None  # actually TextIO

    def open(self) -> None:
        """Open the trace file for appending."""
        os.makedirs(os.path.dirname(self.path) or ".", exist_ok=True)
        self._file = open(self.path, "a")

    def write(self, result: Result) -> None:
        """Write a single result as a JSON line."""
        if self._file is None:
            self.open()
        line = json.dumps(result.to_trace(), ensure_ascii=False)
        print(line, file=self._file)

    def close(self) -> None:
        """Close the trace file."""
        if self._file is not None:
            self._file.close()
            self._file = None

    def flush(self) -> None:
        """Flush the trace file."""
        if self._file is not None:
            self._file.flush()

    def __enter__(self):
        self.open()
        return self

    def __exit__(self, *args):
        self.close()


def load_trace(path: str) -> List[Result]:
    """Load a JSONL trace file into a list of Results."""
    results = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                results.append(Result.from_trace(data))
            except (json.JSONDecodeError, KeyError) as e:
                logger.warning("skipping malformed trace line: %s", e)
    return results


def trace_path(run_id: str) -> str:
    """Return the canonical trace path for a run ID."""
    os.makedirs(TRACE_DIR, exist_ok=True)
    return os.path.join(TRACE_DIR, f"{run_id}.jsonl")
