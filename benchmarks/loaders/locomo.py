"""Backboard-LoCoMo dataset loader (stub).

LoCoMo is a longer-context benchmark. This loader provides the class
skeleton for future implementation. The structure follows LongMemEval
but with different JSON schema.
"""

from __future__ import annotations

import logging
from typing import List

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")


class LoCoMoLoader(DatasetLoader):
    """Placeholder for Backboard-LoCoMo dataset support.

    Expected structure:
        <dataset_path>/<session_id>/conversation.json
        <dataset_path>/<session_id>/questions.json
    """

    def __init__(self, dataset_path: str, config_label: str = "D"):
        self.dataset_path = dataset_path
        self.config_label = config_label

    def name(self) -> str:
        return "locomo"

    def load(self) -> List[Conversation]:
        logger.warning("LoCoMo loader is a stub — no conversations loaded")
        return []

    def ingest_strategy(self, conversation: Conversation, config_label: str) -> IngestPlan:
        return IngestPlan(
            vault=conversation.vault,
            conversations=[conversation],
            auto_extract=True,
            config_label=config_label,
        )

    def questions(self, conversation: Conversation) -> List[Question]:
        return []
