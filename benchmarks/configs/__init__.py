"""Accuracy benchmark configurations.

Four configurations (A, B, C, D) test progressively more of the Ragamuffin
stack. Each config maps to specific ingest parameters and /ask modes.
"""

from __future__ import annotations

from enum import Enum
from typing import Dict, Optional


class Config(Enum):
    """Benchmark configuration enum.

    | Config | Name | What it tests |
    |--------|------|---------------|
    | A | Baseline | Pure /ask (recall + synthesize). No fact extraction. |
    | B | Recall + Facts | /ask + auto_extract: true on ingest |
    | C | Tiered | /ask mode: auto — falls back to full-document on low confidence |
    | D | Full Stack | /ask + facts + fact graph traversal |
    """

    A = "A"
    B = "B"
    C = "C"
    D = "D"

    @classmethod
    def parse(cls, value: str) -> Optional["Config"]:
        """Parse a config label string, case-insensitive."""
        upper = value.upper().strip()
        for member in cls:
            if member.value == upper:
                return member
        return None

    @classmethod
    def all(cls) -> list["Config"]:
        return [cls.A, cls.B, cls.C, cls.D]

    @property
    def label(self) -> str:
        return self.value

    @property
    def ask_mode(self) -> str:
        """Return the /ask mode for this config."""
        if self == Config.C:
            return "auto"
        return "rag"

    @property
    def auto_extract(self) -> bool:
        """Whether fact extraction should be enabled on ingest."""
        return self in (Config.B, Config.D)

    @property
    def full_stack(self) -> bool:
        """Whether fact graph traversal is enabled."""
        return self == Config.D

    @property
    def description(self) -> str:
        return {
            Config.A: "Baseline: pure /ask (recall + synthesize)",
            Config.B: "Recall + Facts: /ask + auto_extract on ingest",
            Config.C: "Tiered: /ask mode=auto, falls back to full-document",
            Config.D: "Full Stack: /ask + facts + fact graph traversal",
        }[self]

    def to_meta(self) -> Dict[str, object]:
        return {
            "config": self.value,
            "ask_mode": self.ask_mode,
            "auto_extract": self.auto_extract,
            "full_stack": self.full_stack,
            "description": self.description,
        }
