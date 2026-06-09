"""Checkpoint save/resume for long benchmark runs.

Saves checkpoint as JSON after every N questions. Resume via --resume flag.
"""

from __future__ import annotations

import json
import logging
import os
from typing import Dict, List, Optional, Set

from .types import Result

logger = logging.getLogger("ragamuffin.benchmark")

CHECKPOINT_DIR = os.environ.get("RAGAMUFFIN_CHECKPOINT_DIR", ".benchmark_checkpoints")


class CheckpointManager:
    """Manages checkpoint save/resume for a benchmark run.

    Checkpoints are stored as JSON in CHECKPOINT_DIR/{run_id}/checkpoint.json.
    """

    def __init__(self, run_id: str, interval: int = 50):
        self.run_id = run_id
        self.interval = interval
        self.dir = os.path.join(CHECKPOINT_DIR, run_id)
        self._completed: Set[str] = set()  # question IDs already answered

    # ── Public API ────────────────────────────────────────────────────────────

    def init(self) -> None:
        """Create checkpoint directory if needed."""
        os.makedirs(self.dir, exist_ok=True)

    def is_completed(self, question_id: str) -> bool:
        """Check if a question has already been answered."""
        return question_id in self._completed

    def save(self, results: List[Result]) -> None:
        """Save checkpoint after every interval questions."""
        path = os.path.join(self.dir, "checkpoint.json")
        data = self._build_checkpoint(results)
        with open(path, "w") as f:
            json.dump(data, f, indent=2)
        logger.info("checkpoint saved: %d results", len(results))

    def load(self) -> Optional[List[Result]]:
        """Load checkpoint from disk, returning completed results."""
        path = os.path.join(self.dir, "checkpoint.json")
        if not os.path.exists(path):
            return None
        with open(path) as f:
            data = json.load(f)
        results = [Result.from_trace(r) for r in data.get("results", [])]
        self._completed = {r.question.id for r in results}
        logger.info(
            "resumed from checkpoint: %d completed questions",
            len(self._completed),
        )
        return results

    def should_save(self, count: int) -> bool:
        """Return True if we should checkpoint after this result."""
        return count > 0 and count % self.interval == 0

    # ── Internal ──────────────────────────────────────────────────────────────

    def _build_checkpoint(self, results: List[Result]) -> Dict:
        return {
            "run_id": self.run_id,
            "total": len(results),
            "results": [r.to_trace() for r in results],
        }
