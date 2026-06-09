"""DatasetLoader abstract base class.

All benchmark dataset loaders implement this interface.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Dict

from benchmarks.core.types import Conversation, IngestPlan, Question


class DatasetLoader(ABC):
    """Abstract base for loading benchmark datasets.

    A loader knows how to:
    1. Discover and load conversations from a dataset directory
    2. Determine the ingest strategy for each conversation
    3. Extract questions with ground truth answers
    """

    @abstractmethod
    def name(self) -> str:
        """Return the benchmark name (e.g. 'longmemeval', 'locomo')."""
        ...

    @abstractmethod
    def load(self) -> List[Conversation]:
        """Load all conversations from the configured dataset path."""
        ...

    @abstractmethod
    def ingest_strategy(
        self,
        conversation: Conversation,
        config_label: str,
    ) -> IngestPlan:
        """Return the ingest plan for a conversation under a given config.

        The plan specifies vault target, whether to auto-extract facts, etc.
        """
        ...

    @abstractmethod
    def questions(self, conversation: Conversation) -> List[Question]:
        """Return all questions with ground truth for a conversation."""
        ...

    def ingest_tags(self, conversation: Conversation, config_label: str) -> Dict[str, str]:
        """Return base tags for ingested content."""
        return {
            "benchmark": self.name(),
            "conversation": conversation.id,
            "config": config_label,
        }
