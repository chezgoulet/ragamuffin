"""Core dataclasses for benchmark results and trace records."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field, asdict
from typing import Dict, List, Optional


class StressProfile(ABC):
    """Interface for stress test implementations.

    All stress tests must implement this interface so the runner can
    discover and execute them polymorphically.  Each test produces a
    ``StressResult``.
    """

    @abstractmethod
    def name(self) -> str:
        """Human-readable name for the test (used in output paths)."""
        ...

    @abstractmethod
    def run(self) -> "StressResult":
        """Execute the stress test and return results."""
        ...


@dataclass
class Question:
    """A single benchmark question with ground truth."""

    id: str
    benchmark: str
    config_label: str  # A, B, C, D
    question_type: str
    text: str
    ground_truth: str
    conversation_id: str  # links back to the Conversation


@dataclass
class Conversation:
    """A complete conversation to ingest into a vault."""

    id: str
    messages: List[Dict]  # list of {"role": ..., "content": ...}
    vault: str  # target vault name
    source: str = ""  # optional source identifier


@dataclass
class IngestPlan:
    """Instructions for how to ingest a set of conversations."""

    vault: str
    conversations: List[Conversation]
    auto_extract: bool
    config_label: str


@dataclass
class Result:
    """The result of asking Ragamuffin a single benchmark question."""

    question: Question
    answer: str
    correct: bool
    latency_ms: float
    retries: int
    error: Optional[str] = None
    sources: List[Dict] = field(default_factory=list)
    score: float = 0.0  # from judge (0.0-1.0)

    def to_trace(self) -> Dict:
        """Serialize to the JSONL trace format."""
        return {
            "question_id": self.question.id,
            "benchmark": self.question.benchmark,
            "config": self.question.config_label,
            "question_type": self.question.question_type,
            "question": self.question.text,
            "ground_truth": self.question.ground_truth,
            "ragamuffin_answer": self.answer,
            "correct": self.correct,
            "latency_ms": self.latency_ms,
            "retries": self.retries,
            "error": self.error,
            "sources": self.sources,
            "vault": self.question.conversation_id,
        }

    @classmethod
    def from_trace(cls, data: Dict) -> "Result":
        """Deserialize from a trace dict."""
        q = Question(
            id=data["question_id"],
            benchmark=data["benchmark"],
            config_label=data["config"],
            question_type=data.get("question_type", "unknown"),
            text=data["question"],
            ground_truth=data["ground_truth"],
            conversation_id=data.get("vault", ""),
        )
        return cls(
            question=q,
            answer=data["ragamuffin_answer"],
            correct=data["correct"],
            latency_ms=data["latency_ms"],
            retries=data.get("retries", 0),
            error=data.get("error"),
            sources=data.get("sources", []),
        )


@dataclass
class StressResult:
    """Aggregate results from a stress test profile."""

    name: str
    total_requests: int
    success_count: int
    error_count: int
    latency_p50: float
    latency_p95: float
    latency_p99: float
    throughput_rps: float
    errors: List[Dict] = field(default_factory=list)

    def to_dict(self) -> Dict:
        return asdict(self)
