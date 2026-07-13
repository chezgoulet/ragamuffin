"""Ragamuffin memory plugin — MemoryProvider for Ragamuffin-backed agent memory.

Provides per-agent Qdrant-isolated memory with automatic session persistence,
cross-agent recall, and semantic search via Ragamuffin's HTTP API.

Config via environment variables (profile-scoped via each profile's .env):
  RAGAMUFFIN_ENDPOINT        — Ragamuffin server URL (default: http://ragamuffin:8000)
  RAGAMUFFIN_AUTH_TOKEN      — API key / JWT for authenticated deployments (optional)
  RAGAMUFFIN_VAULT_PREFIX    — Prefix for agent vault names (default: agent::)
  RAGAMUFFIN_CONFIG          — Path to JSON config file (optional, overrides HERMES_HOME/ragamuffin.json)

Phase 2 — Auto-Injection + Cadence:
  RAGAMUFFIN_RECALL_MODE      — 'hybrid' (default), 'context', or 'tools'
  RAGAMUFFIN_SAVE_MESSAGES    — 'true' (default) / 'false'
  RAGAMUFFIN_INJECTION_FREQ   — 'every_turn' (default) or 'first_turn'
  RAGAMUFFIN_CONTEXT_CADENCE  — Refresh base context every N turns (default 3, 0=disable)
  RAGAMUFFIN_DIALECTIC_CADENCE— Refresh dialectic every N turns (default 5, 0=disable)

Phase 3 — Dialectic Depth + Polish:
  RAGAMUFFIN_DIALECTIC_DEPTH   — Multi-pass reasoning levels: 1 (cold, default), 2 (+warm), or 3 (+hot)
  RAGAMUFFIN_EMPTY_STREAK_BACKOFF — 'true' (default) / 'false' — widen cadence on silent responses

Phase 4 — Beat Honcho:
  RAGAMUFFIN_CROSS_AGENT_VAULTS — Comma-separated list of other agent vaults for cross-recall (default: '')
  RAGAMUFFIN_FACT_GRAPH_ENABLED — 'true' (default) / 'false' — inject fact graph chains into context

Config file ($HERMES_HOME/ragamuffin.json) — matches honcho.json structure:
  {
    "endpoint": "http://ragamuffin:8000",
    "auth_token": "...",
    "vault_prefix": "agent::",
    "recall_mode": "hybrid",
    "save_messages": true,
    "injection_frequency": "every_turn",
    "context_cadence": 3,
    "dialectic_cadence": 5
  }

Environment variables override config file values.

Lifecycle:
  initialize()       → POST /v1/vaults (create/confirm vault), load config
  prefetch(query)    → returns cached layered context via _wrap_context()
  queue_prefetch()   → background thread → POST /v1/recall (cadence-gated)
  _refresh_context() → rebuilds base context cache on cadence
  sync_turn()        → background thread → POST /v1/ingest (cadence-gated, saveMessages)
  on_session_end()   → POST /v1/ingest (session summary) + auto-extracted
                        decision/conclusion/config/preference facts via
                        POST /v1/facts (deduped by deterministic key)
  handle_tool_call   → POST /v1/recall against specified vault

recallMode routing:
  hybrid  — inject context + expose tools (default)
  context — inject context only, hide tools
  tools   — expose tools only, no auto-injection

Tool schemas:
  ragamuffin_recall     — search any agent's vault (cross-agent recall)
  ragamuffin_search    — alias for ragamuffin_recall
  ragamuffin_ask       — synthesis with citations (supports reasoning_effort)
  ragamuffin_profile   — get/set agent peer card
  ragamuffin_context   — composite context (card + summary + recall)
  ragamuffin_learn     — store a conclusion as a fact
  ragamuffin_fact_*    — fact CRUD and graph operations
  ragamuffin_review_*  — review queue management
"""

from __future__ import annotations

import json
import logging
import os
import re
import threading
import time
from typing import Any, Dict, List, Optional

from agent.memory_provider import MemoryProvider

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------

_DEFAULT_ENDPOINT = "http://ragamuffin:8000"
_VAULT_PREFIX = "agent::"
_REQUEST_TIMEOUT = 15.0  # seconds
_PREFETCH_TIMEOUT = 3.0  # seconds to wait for background thread
_PEER_CARD_PREFIX = "peer/{agent}/card/"  # peer card fact key prefix

# ---------------------------------------------------------------------------
# Tool schema
# ---------------------------------------------------------------------------

RECALL_SCHEMA = {
    "name": "ragamuffin_recall",
    "description": (
        "Semantic search across any agent's Ragamuffin vault. "
        "Use this to recall what another agent knows, or to find specific "
        "information from past sessions. "
        "Returns ranked text excerpts with relevance scores."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "vault": {
                "type": "string",
                "description": (
                    "Target agent vault to search. "
                    "Use 'agent::<name>' format. "
                    "Common values: agent::dev, agent::robot, agent::scout, "
                    "agent::press, agent::pulse. "
                    "Omit to search your own vault."
                ),
            },
            "query": {
                "type": "string",
                "description": "Natural language search query.",
            },
            "limit": {
                "type": "integer",
                "description": "Maximum results to return (1-20, default 5).",
            },
            "min_score": {
                "type": "number",
                "description": "Minimum relevance threshold 0.0-1.0 (default 0.0).",
            },
        },
        "required": ["query"],
    },
}

SEARCH_SCHEMA = {
    "name": "ragamuffin_search",
    "description": (
        "Alias for ragamuffin_recall. Semantic search across any agent's "
        "Ragamuffin vault. Returns ranked text excerpts with relevance scores."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "vault": {
                "type": "string",
                "description": (
                    "Target agent vault to search. "
                    "Use 'agent::<name>' format. "
                    "Omit to search your own vault."
                ),
            },
            "query": {
                "type": "string",
                "description": "Natural language search query.",
            },
            "limit": {
                "type": "integer",
                "description": "Maximum results to return (1-20, default 5).",
            },
            "min_score": {
                "type": "number",
                "description": "Minimum relevance threshold 0.0-1.0 (default 0.0).",
            },
        },
        "required": ["query"],
    },
}

ASK_SCHEMA = {
    "name": "ragamuffin_ask",
    "description": (
        "Ask a synthesis question across agent memory. "
        "Returns a natural language answer with citations from relevant "
        "vault content. Use for questions that need synthesis across "
        "multiple pieces of information, rather than raw recall."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Natural language question to synthesize across memory.",
            },
            "mode": {
                "type": "string",
                "description": "Synthesis mode (default: 'standard').",
                "enum": ["standard", "concise", "detailed"],
            },
            "top_k": {
                "type": "integer",
                "description": "Number of relevant chunks to retrieve (1-20, default 5).",
            },
            "reasoning_effort": {
                "type": "string",
                "description": "Reasoning effort / depth for synthesis (default: 'auto').",
                "enum": ["auto", "low", "medium", "high"],
            },
        },
        "required": ["query"],
    },
}

PROFILE_SCHEMA = {
    "name": "ragamuffin_profile",
    "description": (
        "Get or update the agent's peer profile card. "
        "Without a value, returns the current card. "
        "With a value, updates the card with a description of what this "
        "agent knows, does, and how to interact with it."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "value": {
                "type": "string",
                "description": "New profile card content (omit to read current card).",
            },
        },
    },
}

CONTEXT_SCHEMA = {
    "name": "ragamuffin_context",
    "description": (
        "Get a composite context bundle for this agent: the peer card, "
        "session summary, and relevant recalled information. "
        "Use this to quickly orient yourself to an agent's state."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Optional query to focus recall within the context bundle.",
            },
            "top_k": {
                "type": "integer",
                "description": "Number of recall results to include (1-10, default 3).",
            },
        },
    },
}

LEARN_SCHEMA = {
    "name": "ragamuffin_learn",
    "description": (
        "Store a conclusion or learned fact from the current conversation. "
        "Use this when you discover something the agent should remember "
        "permanently - a user preference, a decision, an observation. "
        "The statement is saved as a persisted fact."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "statement": {
                "type": "string",
                "description": "The conclusion or fact to remember.",
            },
            "tags": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Optional tags for categorization.",
            },
            "confidence": {
                "type": "number",
                "description": "Confidence 0.0-1.0 (default 0.7).",
            },
        },
        "required": ["statement"],
    },
}

FACT_GET_SCHEMA = {
    "name": "ragamuffin_fact_get",
    "description": (
        "Retrieve a specific fact by its key. Returns the fact value, "
        "confidence (0.0-1.0), TTL, status, and relationships. "
        "Use when you need the full detail of a known fact rather than "
        "semantic search."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "key": {
                "type": "string",
                "description": "Fact key to retrieve (e.g., 'user_preference_timezone').",
            },
        },
        "required": ["key"],
    },
}

FACT_PUT_SCHEMA = {
    "name": "ragamuffin_fact_put",
    "description": (
        "Write or update a fact in the agent's own vault. "
        "Use to record persistent knowledge - user preferences, "
        "decisions made, learned patterns, or any structured information "
        "the agent should remember across sessions."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "key": {
                "type": "string",
                "description": "Unique key for the fact (snake_case, descriptive).",
            },
            "value": {
                "type": "string",
                "description": "The fact value / statement to store.",
            },
            "confidence": {
                "type": "number",
                "description": "Confidence 0.0-1.0 (default 0.7).",
            },
            "ttl_days": {
                "type": "integer",
                "description": "Days until expiry (0 = no expiry, default 365).",
            },
            "tags": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Optional tags for categorization.",
            },
            "source": {
                "type": "string",
                "description": "Source context (e.g., 'session', 'observation').",
            },
        },
        "required": ["key", "value"],
    },
}

FACT_GRAPH_SCHEMA = {
    "name": "ragamuffin_fact_graph",
    "description": (
        "Get the lineage graph of a fact - what it supersedes, "
        "contradicts, or refines. Use to understand how a fact has "
        "evolved over time or to resolve conflicting information."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "key": {
                "type": "string",
                "description": "Fact key to get the lineage graph for.",
            },
        },
        "required": ["key"],
    },
}

REVIEW_LIST_SCHEMA = {
    "name": "ragamuffin_review_list",
    "description": (
        "List flagged facts awaiting review. Facts enter the review queue "
        "when the pruner detects contradictions, low confidence, or near-expiry. "
        "Returns facts with their review reason, confidence, and creation time."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "reason": {
                "type": "string",
                "description": "Filter by review reason (e.g., 'contradiction', 'low_confidence', 'expiring').",
            },
            "limit": {
                "type": "integer",
                "description": "Maximum results to return (1-100, default 20).",
            },
        },
    },
}

REVIEW_RESOLVE_SCHEMA = {
    "name": "ragamuffin_review_resolve",
    "description": (
        "Resolve a flagged fact in the review queue. Actions: confirm the fact, "
        "supersede it with a corrected version, or reject it as invalid."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "point_id": {
                "type": "string",
                "description": "The Qdrant point ID of the fact to resolve.",
            },
            "action": {
                "type": "string",
                "description": "Resolution action: 'confirm', 'supersede', or 'reject'.",
                "enum": ["confirm", "supersede", "reject"],
            },
            "correction": {
                "type": "string",
                "description": "Corrected fact value (required for 'supersede' action).",
            },
        },
        "required": ["point_id", "action"],
    },
}

# ragamuffin_status — health introspection (#784)
STATUS_SCHEMA = {
    "name": "ragamuffin_status",
    "description": (
        "Check Ragamuffin provider health and connectivity. "
        "Returns server status, tool injection state, vault info, "
        "and last context refresh turn. Lightweight — no side effects."
    ),
    "parameters": {
        "type": "object",
        "properties": {},
    },
}

ALL_TOOL_SCHEMAS = [
    RECALL_SCHEMA,
    SEARCH_SCHEMA,
    ASK_SCHEMA,
    PROFILE_SCHEMA,
    CONTEXT_SCHEMA,
    LEARN_SCHEMA,
    FACT_GET_SCHEMA,
    FACT_PUT_SCHEMA,
    FACT_GRAPH_SCHEMA,
    REVIEW_LIST_SCHEMA,
    REVIEW_RESOLVE_SCHEMA,
    STATUS_SCHEMA,
]


# ---------------------------------------------------------------------------
# HTTP helper
# ---------------------------------------------------------------------------

def _get_requests():
    """Lazy import requests."""
    try:
        import requests as req
        return req
    except ImportError:
        return None


def _build_headers(auth_token: str = "") -> dict:
    headers = {"Content-Type": "application/json"}
    if auth_token:
        headers["Authorization"] = f"Bearer {auth_token}"
    return headers


def _build_endpoint(base: str, path: str) -> str:
    base = base.rstrip("/")
    path = path.lstrip("/")
    return f"{base}/{path}"


# ---------------------------------------------------------------------------
# MemoryProvider implementation
# ---------------------------------------------------------------------------

class RagamuffinMemoryProvider(MemoryProvider):
    """Per-agent Qdrant-isolated memory via Ragamuffin."""

    def __init__(self):
        self._endpoint = ""
        self._auth_token = ""
        self._vault_prefix = _VAULT_PREFIX
        self._agent_vault = ""   # resolved vault name, e.g. "agent::dev"
        self._agent_identity = ""  # e.g. "dev"
        self._requests = None
        self._config_path = ""

        # Liveness tracking
        self._available = False
        self._vault_ready = False

        # Prefetch state
        self._prefetch_result = ""
        self._prefetch_lock = threading.Lock()
        self._prefetch_thread: Optional[threading.Thread] = None

        # Sync state
        self._sync_thread: Optional[threading.Thread] = None

        # Turn tracking for session end
        self._turn_counter = 0
        self._session_id = ""

        # Phase 2 — Auto-Injection + Cadence
        self._recall_mode = "hybrid"           # hybrid | context | tools
        self._save_messages = True
        self._injection_frequency = "every_turn"  # every_turn | first_turn
        self._context_cadence = 3              # refresh base context every N turns
        self._dialectic_cadence = 5            # refresh dialectic every N turns
        self._base_context_cache = ""
        self._pending_dialectic = ""
        self._context_cache_turn = 0           # last turn context was refreshed
        self._dialectic_cache_turn = 0         # last turn dialectic was refreshed

        # Phase 3 — Dialectic Depth + Polish
        self._dialectic_depth = 1              # multi-pass levels: 1=cold, 2=cold+warm, 3=cold+warm+hot
        self._empty_streak_backoff = True
        self._empty_streak = 0                 # consecutive silent user responses
        self._max_empty_streak = 3             # threshold for cadence multiplication
        self._trivial_patterns: List[str] = []

        # Phase 4 — Beat Honcho
        self._cross_agent_vaults: List[str] = []  # other vaults for cross-agent recall
        self._graph_depth = 1  # how deep to traverse fact graph
        self._fact_graph_enabled = True
        self._cross_recall_enabled = True

        # Context bundle cache
        self._context_bundle: Dict[str, Any] = {}

        # Session-end auto fact extraction (issue #793)
        self._auto_session_facts = True
        self._session_facts_prefix = "house"

    # -- Identity -----------------------------------------------------------

    @property
    def name(self) -> str:
        return "ragamuffin"

    # -- Availability check -------------------------------------------------

    @staticmethod
    def _config_file_path() -> str:
        """Resolve the path to the optional Ragamuffin JSON config file.

        Honors ``RAGAMUFFIN_CONFIG`` first, then falls back to
        ``$HERMES_HOME/ragamuffin.json``. Returns ``""`` when none is set or
        the file does not exist.
        """
        config_path = os.environ.get("RAGAMUFFIN_CONFIG", "")
        if not config_path:
            hermes_home = os.environ.get("HERMES_HOME", "")
            if hermes_home:
                config_path = os.path.join(hermes_home, "ragamuffin.json")
        if config_path and os.path.exists(config_path):
            return config_path
        return ""

    def is_available(self) -> bool:
        """Return True if Ragamuffin is configured.

        Configured means either ``RAGAMUFFIN_ENDPOINT`` is set, or a valid
        config file exists at ``RAGAMUFFIN_CONFIG`` / ``$HERMES_HOME/ragamuffin.json``.
        Previously only the env var was checked, so config-file-only setups
        silently never registered (#781).
        """
        if os.environ.get("RAGAMUFFIN_ENDPOINT"):
            return True
        return bool(self._config_file_path())

    # -- Config schema (for `hermes memory setup`) --------------------------

    def get_config_schema(self) -> List[Dict[str, Any]]:
        return [
            {
                "key": "endpoint",
                "description": "Ragamuffin server URL",
                "required": True,
                "default": _DEFAULT_ENDPOINT,
                "env_var": "RAGAMUFFIN_ENDPOINT",
            },
            {
                "key": "auth_token",
                "description": (
                    "Ragamuffin auth token (API key or JWT). "
                    "Leave blank if auth is disabled on your server."
                ),
                "secret": True,
                "env_var": "RAGAMUFFIN_AUTH_TOKEN",
            },
            {
                "key": "vault_prefix",
                "description": (
                    "Prefix for agent vault names. "
                    "Default: 'agent::' creates vaults like 'agent::dev'."
                ),
                "default": _VAULT_PREFIX,
                "env_var": "RAGAMUFFIN_VAULT_PREFIX",
            },
            {
                "key": "recall_mode",
                "description": (
                    "Toggle injection vs tool visibility: 'hybrid' (inject + tools), "
                    "'context' (inject only, hide tools), 'tools' (no auto-injection)."
                ),
                "default": "hybrid",
                "env_var": "RAGAMUFFIN_RECALL_MODE",
            },
            {
                "key": "save_messages",
                "description": (
                    "Persist turn content to vault. Set to false when you only "
                    "need selective fact storage via ragamuffin_learn."
                ),
                "default": True,
                "env_var": "RAGAMUFFIN_SAVE_MESSAGES",
            },
            {
                "key": "injection_frequency",
                "description": (
                    "'every_turn' injects context on every turn. 'first_turn' "
                    "only injects on the first turn of a session."
                ),
                "default": "every_turn",
                "env_var": "RAGAMUFFIN_INJECTION_FREQ",
            },
            {
                "key": "context_cadence",
                "description": (
                    "Refresh base context (peer card + session summary) "
                    "every N turns. 0 disables auto-refresh."
                ),
                "default": 3,
                "env_var": "RAGAMUFFIN_CONTEXT_CADENCE",
            },
            {
                "key": "dialectic_cadence",
                "description": (
                    "Refresh dialectic reasoning context every N turns. "
                    "0 disables. Phase 3 extends this with multi-pass depth."
                ),
                "default": 5,
                "env_var": "RAGAMUFFIN_DIALECTIC_CADENCE",
            },
            {
                "key": "dialectic_depth",
                "description": (
                    "Multi-pass reasoning levels. 1=cold (analytical), "
                    "2=cold+warm (analytical+creative), "
                    "3=cold+warm+hot (analytical+creative+evaluative). "
                    "Higher depth produces richer dialectic context."
                ),
                "default": 1,
                "env_var": "RAGAMUFFIN_DIALECTIC_DEPTH",
            },
            {
                "key": "empty_streak_backoff",
                "description": (
                    "Widen dialectic cadence on consecutive silent or trivial "
                    "responses from the user. After 3 silent turns, cadence "
                    "doubles to avoid wasting context on non-productive exchanges."
                ),
                "default": True,
                "env_var": "RAGAMUFFIN_EMPTY_STREAK_BACKOFF",
            },
            {
                "key": "cross_agent_vaults",
                "description": (
                    "Comma-separated list of other agent vaults to "
                    "cross-reference during auto-injection. "
                    "E.g. 'agent::robot,agent::scout'. "
                    "Related facts from these vaults are surfaced in context."
                ),
                "default": "",
                "env_var": "RAGAMUFFIN_CROSS_AGENT_VAULTS",
            },
            {
                "key": "fact_graph_enabled",
                "description": (
                    "Inject fact graph chains (supersession, refinement) "
                    "into auto-injected context. When enabled, known facts "
                    "include their provenance chain."
                ),
                "default": True,
                "env_var": "RAGAMUFFIN_FACT_GRAPH_ENABLED",
            },
            {
                "key": "auto_session_facts",
                "description": (
                    "At session end, automatically extract key decisions, "
                    "conclusions, config, and preferences from the transcript "
                    "and write them to the vault as deduplicated facts "
                    "(house/<domain>/<topic>). No manual ragamuffin_learn "
                    "call required."
                ),
                "default": True,
                "env_var": "RAGAMUFFIN_AUTO_SESSION_FACTS",
            },
            {
                "key": "session_facts_prefix",
                "description": (
                    "Key namespace prefix for auto-extracted session facts. "
                    "Produces keys like '<prefix>/decision/<topic>'."
                ),
                "default": "house",
                "env_var": "RAGAMUFFIN_SESSION_FACTS_PREFIX",
            },
        ]

    # -- Config file loading -------------------------------------------------

    def _load_config_file(self) -> None:
        """Load config from $HERMES_HOME/ragamuffin.json if present.

        File format (matches honcho.json structure):
        {
          "endpoint": "http://ragamuffin:8000",
          "auth_token": "...",
          "vault_prefix": "agent::"
        }

        Environment variables take precedence over file values.
        """
        config_path = self._config_file_path()
        if not config_path:
            return

        self._config_path = config_path
        try:
            with open(config_path, "r") as f:
                cfg = json.load(f)

            # Apply file values only when env var is not set
            if "endpoint" in cfg and "RAGAMUFFIN_ENDPOINT" not in os.environ:
                self._endpoint = cfg["endpoint"]
            if "auth_token" in cfg and "RAGAMUFFIN_AUTH_TOKEN" not in os.environ:
                self._auth_token = cfg["auth_token"]
            if "vault_prefix" in cfg and "RAGAMUFFIN_VAULT_PREFIX" not in os.environ:
                self._vault_prefix = cfg["vault_prefix"]

            # Phase 2 — recall mode / cadence config (env overrides)
            if "recall_mode" in cfg and "RAGAMUFFIN_RECALL_MODE" not in os.environ:
                val = str(cfg["recall_mode"]).lower()
                if val in ("hybrid", "context", "tools"):
                    self._recall_mode = val
            if "save_messages" in cfg and "RAGAMUFFIN_SAVE_MESSAGES" not in os.environ:
                self._save_messages = bool(cfg["save_messages"])
            if (
                "injection_frequency" in cfg
                and "RAGAMUFFIN_INJECTION_FREQ" not in os.environ
            ):
                val = str(cfg["injection_frequency"]).lower()
                if val in ("every_turn", "first_turn"):
                    self._injection_frequency = val
            if "context_cadence" in cfg and "RAGAMUFFIN_CONTEXT_CADENCE" not in os.environ:
                self._context_cadence = max(0, int(cfg["context_cadence"]))
            if (
                "dialectic_cadence" in cfg
                and "RAGAMUFFIN_DIALECTIC_CADENCE" not in os.environ
            ):
                self._dialectic_cadence = max(0, int(cfg["dialectic_cadence"]))

            # Phase 3 M-bM-^@M-^T dialectic depth + empty streak backoff
            if (
                "dialectic_depth" in cfg
                and "RAGAMUFFIN_DIALECTIC_DEPTH" not in os.environ
            ):
                self._dialectic_depth = max(1, min(3, int(cfg["dialectic_depth"])))
            if (
                "empty_streak_backoff" in cfg
                and "RAGAMUFFIN_EMPTY_STREAK_BACKOFF" not in os.environ
            ):
                self._empty_streak_backoff = bool(cfg["empty_streak_backoff"])

            # Phase 4 — cross-agent vaults + fact graph
            if (
                "cross_agent_vaults" in cfg
                and "RAGAMUFFIN_CROSS_AGENT_VAULTS" not in os.environ
            ):
                raw = cfg["cross_agent_vaults"]
                if isinstance(raw, str):
                    self._cross_agent_vaults = [
                        v.strip() for v in raw.split(",") if v.strip()
                    ]
                elif isinstance(raw, list):
                    self._cross_agent_vaults = [str(v) for v in raw]
            if (
                "fact_graph_enabled" in cfg
                and "RAGAMUFFIN_FACT_GRAPH_ENABLED" not in os.environ
            ):
                self._fact_graph_enabled = bool(cfg["fact_graph_enabled"])

            # Issue #793 — auto session-to-fact storage
            if (
                "auto_session_facts" in cfg
                and "RAGAMUFFIN_AUTO_SESSION_FACTS" not in os.environ
            ):
                self._auto_session_facts = bool(cfg["auto_session_facts"])
            if (
                "session_facts_prefix" in cfg
                and "RAGAMUFFIN_SESSION_FACTS_PREFIX" not in os.environ
            ):
                self._session_facts_prefix = str(
                    cfg["session_facts_prefix"]
                ) or "house"

            logger.debug("Loaded Ragamuffin config from %s", config_path)
        except Exception as e:
            logger.warning(
                "Failed to load Ragamuffin config from %s: %s", config_path, e
            )

    # -- Core lifecycle -----------------------------------------------------

    def initialize(self, session_id: str, **kwargs) -> None:
        """Create/confirm the agent's vault and warm up.

        Reads config from env vars (overrides) or $HERMES_HOME/ragamuffin.json.
        Derives vault name from agent_identity (kwargs) or session_id.
        """
        self._endpoint = os.environ.get("RAGAMUFFIN_ENDPOINT", _DEFAULT_ENDPOINT)
        self._auth_token = os.environ.get("RAGAMUFFIN_AUTH_TOKEN", "")
        self._vault_prefix = os.environ.get("RAGAMUFFIN_VAULT_PREFIX", _VAULT_PREFIX)
        self._session_id = session_id
        self._turn_counter = 0

        # Phase 2 — load recall mode / cadence config from env
        self._recall_mode = os.environ.get("RAGAMUFFIN_RECALL_MODE", "hybrid").lower()
        if self._recall_mode not in ("hybrid", "context", "tools"):
            self._recall_mode = "hybrid"
        save_ms = os.environ.get("RAGAMUFFIN_SAVE_MESSAGES", "true").lower()
        self._save_messages = save_ms in ("true", "1", "yes")
        self._injection_frequency = os.environ.get(
            "RAGAMUFFIN_INJECTION_FREQ", "every_turn"
        ).lower()
        if self._injection_frequency not in ("every_turn", "first_turn"):
            self._injection_frequency = "every_turn"
        try:
            self._context_cadence = int(
                os.environ.get("RAGAMUFFIN_CONTEXT_CADENCE", "3")
            )
        except (ValueError, TypeError):
            self._context_cadence = 3
        try:
            self._dialectic_cadence = int(
                os.environ.get("RAGAMUFFIN_DIALECTIC_CADENCE", "5")
            )
        except (ValueError, TypeError):
            self._dialectic_cadence = 5

        # Phase 3 — dialectic depth
        try:
            d = int(os.environ.get("RAGAMUFFIN_DIALECTIC_DEPTH", "1"))
            self._dialectic_depth = max(1, min(3, d))
        except (ValueError, TypeError):
            self._dialectic_depth = 1
        eb = os.environ.get("RAGAMUFFIN_EMPTY_STREAK_BACKOFF", "true").lower()
        self._empty_streak_backoff = eb in ("true", "1", "yes")
        self._empty_streak = 0

        # Phase 4 — cross-agent vaults + fact graph
        cross_vaults = os.environ.get("RAGAMUFFIN_CROSS_AGENT_VAULTS", "")
        self._cross_agent_vaults = [
            v.strip() for v in cross_vaults.split(",") if v.strip()
        ]
        fg = os.environ.get("RAGAMUFFIN_FACT_GRAPH_ENABLED", "true").lower()
        self._fact_graph_enabled = fg in ("true", "1", "yes")

        # Issue #793 — auto session-to-fact storage
        self._auto_session_facts = os.environ.get(
            "RAGAMUFFIN_AUTO_SESSION_FACTS", "true"
        ).lower() in ("true", "1", "yes")
        self._session_facts_prefix = os.environ.get(
            "RAGAMUFFIN_SESSION_FACTS_PREFIX", "house"
        )

        self._context_cache_turn = 0
        self._dialectic_cache_turn = 0

        # Honor pre-set _requests (e.g. from tests); otherwise lazy-import
        if self._requests is None:
            self._requests = _get_requests()

        # Issue #786 — warn loudly when the provider is configured but the
        # endpoint is missing and no config file resolves. Without this the
        # plugin silently falls back to the built-in backend with zero signal.
        if not self._endpoint:
            cfg_file = self._config_file_path()
            if cfg_file:
                logger.warning(
                    "[Ragamuffin] provider=ragamuffin but endpoint not set; "
                    "will load from config file %s",
                    cfg_file,
                )
            else:
                logger.warning(
                    "[Ragamuffin] provider=ragamuffin but tools cannot load: "
                    "missing RAGAMUFFIN_ENDPOINT and no config file at "
                    "RAGAMUFFIN_CONFIG or $HERMES_HOME/ragamuffin.json"
                )

        if self._requests is None:
            logger.warning(
                "requests library not installed - Ragamuffin plugin disabled"
            )
            self._available = False
            return

        # Try loading from $HERMES_HOME/ragamuffin.json (env vars override)
        self._load_config_file()

        # Resolve agent identity for vault naming
        agent_identity = kwargs.get("agent_identity", "")
        if not agent_identity:
            # Fallback: use session_id or profile name
            agent_identity = kwargs.get("user_id", "") or session_id or "hermes"
        self._agent_identity = agent_identity
        self._agent_vault = f"{self._vault_prefix}{agent_identity}"
        logger.debug(
            "Ragamuffin initialized for agent '%s' -> vault '%s' at %s",
            agent_identity,
            self._agent_vault,
            self._endpoint,
        )

        # Provision vault
        self._provision_vault()

    def _provision_vault(self) -> bool:
        """GET /vaults to confirm the agent's vault is configured.

        If the vault doesn't exist, tries POST /vaults to create it
        dynamically (requires multi-tenant mode).
        exists and the plugin can reach the server.
        """
        if not self._requests:
            return False

        try:
            url = _build_endpoint(self._endpoint, "/vaults")
            headers = _build_headers(self._auth_token)

            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                data = resp.json()
                vaults = data.get("vaults", [])
                # Check if our vault is in the list
                vault_names = [v.get("name", "") for v in vaults]
                if self._agent_vault in vault_names:
                    self._vault_ready = True
                    self._available = True
                    logger.info(
                        "Ragamuffin vault confirmed: %s", self._agent_vault
                    )
                    return True
                else:
                    # Vault not found - try creating it dynamically
                    logger.info(
                        "Ragamuffin vault '%s' not found, attempting creation",
                        self._agent_vault,
                    )
                    return self._create_vault()
            else:
                logger.warning(
                    "Ragamuffin vault check failed: %s %s",
                    resp.status_code,
                    resp.text,
                )
                return False
        except Exception as e:
            logger.warning("Ragamuffin vault check error: %s", e)
            return False

    def _create_vault(self) -> bool:
        """POST /vaults to create this agent's vault dynamically."""
        try:
            url = _build_endpoint(self._endpoint, "/vaults")
            headers = _build_headers(self._auth_token)
            body = {
                "name": self._agent_vault,
                "path": f"/tmp/vault-{self._agent_vault}",
            }
            resp = self._requests.post(
                url, json=body, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code in (200, 201):
                self._vault_ready = True
                self._available = True
                logger.info(
                    "Ragamuffin vault created: %s", self._agent_vault
                )
                return True
            logger.warning(
                "Ragamuffin vault creation failed: %s %s",
                resp.status_code,
                resp.text,
            )
            return False
        except Exception as e:
            logger.warning("Ragamuffin vault creation error: %s", e)
            return False

    def system_prompt_block(self) -> str:
        """Return status block for the system prompt if ready."""
        if not self._available or not self._vault_ready:
            return ""
        block = "# Ragamuffin Agent Memory\nActive.\n"
        if self._save_messages:
            block += "All turns are automatically persisted.\n"

        if self._recall_mode != "tools":
            block += "Context is automatically injected each turn.\n"
        if self._recall_mode != "context":
            block += (
                "Use `ragamuffin_recall` or `ragamuffin_search` "
                "to search any agent's vault.\n"
            )
            block += (
                "Use `ragamuffin_profile` to view or update "
                "this agent's peer card.\n"
            )
            block += (
                "Use `ragamuffin_context` for a composite "
                "context bundle.\n"
            )
            block += (
                "Use `ragamuffin_learn` to store a conclusion "
                "as a persistent fact.\n"
            )
            block += (
                "Use `ragamuffin_fact_get` to retrieve a "
                "specific fact by key.\n"
            )
            block += (
                "Use `ragamuffin_fact_put` to write or update "
                "a fact.\n"
            )
        return block

    # -- Phase 2: Auto-Injection + Cadence ----------------------------------

    def _build_base_context(self) -> str:
        """Build the base context layer: peer card + session summary.

        Phase 4: Also injects:
        - Fact graph chains (supersession, refinement) for known facts
        - Cross-agent recall from other agent vaults
        """
        if not self._requests:
            return ""
        parts = []

        # -- Peer card --
        card = self._get_peer_card()
        if card:
            parts.append(f"--- Peer Card ({self._agent_identity}) ---\n{card}")

        # -- Phase 4: Fact graph chains for known facts --
        if self._fact_graph_enabled and card:
            key = self._get_peer_card_key()
            try:
                url = _build_endpoint(
                    self._endpoint,
                    f"/v1/facts/{key}/graph",
                )
                headers = _build_headers(self._auth_token)
                resp = self._requests.get(
                    url, headers=headers, timeout=_REQUEST_TIMEOUT
                )
                if resp.status_code == 200:
                    graph_data = resp.json()
                    edges = graph_data.get("edges", []) or graph_data.get("related", [])
                    if edges:
                        lines = ["--- Fact Chain ---"]
                        for edge in edges:
                            rel = edge.get("relation", "related")
                            related_key = edge.get("key", "") or edge.get("fact", "")
                            related_val = edge.get("value", "") or edge.get("summary", "")
                            if related_val:
                                lines.append(f"- {rel}: {related_val}")
                        if len(lines) > 1:
                            parts.append("\n".join(lines))
            except Exception as e:
                logger.debug("Fact graph fetch error: %s", e)

        # -- Session summary --
        try:
            url = _build_endpoint(
                self._endpoint,
                f"/v1/facts?prefix=session/{self._session_id}&limit=1",
            )
            headers = _build_headers(self._auth_token)
            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                data = resp.json()
                facts = data.get("facts", []) or data.get("results", [])
                if facts:
                    summary = facts[-1].get("value", "")
                    if summary:
                        parts.append(f"--- Session Summary ---\n{summary}")
        except Exception as e:
            logger.debug("Base context summary fetch error: %s", e)

        # -- Phase 4: Cross-agent recall --
        if self._cross_agent_vaults:
            for other_vault in self._cross_agent_vaults:
                try:
                    url = _build_endpoint(
                        self._endpoint,
                        f"/vault/{other_vault}/recall",
                    )
                    headers = _build_headers(self._auth_token)
                    # Use a broad query to surface recent facts
                    payload = {
                        "query": "*",
                        "limit": 3,
                    }
                    resp = self._requests.post(
                        url,
                        json=payload,
                        headers=headers,
                        timeout=_REQUEST_TIMEOUT,
                    )
                    if resp.status_code == 200:
                        data = resp.json()
                        results = data.get("results", []) or data.get("facts", [])
                        if results:
                            x_lines = [f"--- Recent: {other_vault} ---"]
                            for r in results[:3]:
                                val = r.get("value", "") or r.get("text", "")
                                if len(val) > 200:
                                    val = val[:200] + "..."
                                if val:
                                    x_lines.append(f"- {val}")
                            if len(x_lines) > 1:
                                parts.append("\n".join(x_lines))
                except Exception as e:
                    logger.debug("Cross-agent recall error for %s: %s", other_vault, e)

        return "\n\n".join(parts)

    def _build_dialectic(self) -> str:
        """Build the dialectic reasoning context.

        Multi-pass dialectic with cold/warm/hot reasoning levels,
        bail-out heuristic, and empty-streak backoff.

        Each pass produces a structured <dialectic-pass> block that guides
        the model to reason from a specific perspective. Higher depth
        produces more passes. Bail-out skips warm/hot passes when the
        cold pass already indicates strong signal (high info density).

        Returns:
            str — concatenated dialectic passes, or empty if frozen.
        """
        if not self._requests:
            return ""

        depth = self._dialectic_depth
        if depth < 1:
            return ""

        passes = []

        # ── Pass 1: Cold — Analytical/Logical ──
        cold = self._build_cold_pass()
        passes.append(cold)

        # Bail-out: if cold pass detected strong signal (high contradiction density
        # or high fact discovery), skip deeper passes to save context.
        if depth >= 2 and not self._should_bail_out(cold):
            warm = self._build_warm_pass()
            passes.append(warm)

        if depth >= 3 and not self._should_bail_out(passes[-1]):
            hot = self._build_hot_pass()
            passes.append(hot)

        return "\n\n".join(p for p in passes if p)

    def _build_cold_pass(self) -> str:
        """Cold (analytical/logical) reasoning pass.

        Prompts the model to:
        - Extract specific facts from context
        - Identify contradictions between facts
        - Detect stale or superseded information
        - Surface gaps in knowledge
        """
        return (
            "<dialectic-pass level=\"cold\" role=\"analytical\">\n"
            "# Analytical Reasoning\n"
            "Review the context below. Identify:\n"
            "- Specific verifiable facts you can extract\n"
            "- Contradictions or inconsistencies\n"
            "- Stale or out-of-date information\n"
            "- Gaps where information is missing\n"
            "</dialectic-pass>"
        )

    def _build_warm_pass(self) -> str:
        """Warm (creative/synthetic) reasoning pass.

        Prompts the model to:
        - Generate hypotheses from incomplete data
        - Connect seemingly unrelated facts
        - Infer user intent or patterns
        - Suggest new facts to learn
        """
        return (
            "<dialectic-pass level=\"warm\" role=\"synthetic\">\n"
            "# Synthetic Reasoning\n"
            "Draw connections from the context below. Consider:\n"
            "- What underlying patterns emerge?\n"
            "- What hypotheses explain the data?\n"
            "- What related information might be useful?\n"
            "- What should this agent learn next?\n"
            "</dialectic-pass>"
        )

    def _build_hot_pass(self) -> str:
        """Hot (evaluative) reasoning pass.

        Prompts the model to:
        - Assess confidence in extracted facts
        - Prioritize which facts are most important
        - Decide which facts need confirmation
        - Summarize the state of knowledge
        """
        return (
            "<dialectic-pass level=\"hot\" role=\"evaluative\">\n"
            "# Evaluative Reasoning\n"
            "Assess the quality of information below:\n"
            "- Rate confidence in each fact (0.0-1.0)\n"
            "- Which facts are most critical to remember?\n"
            "- Which facts need verification?\n"
            "- Summarize the current state of knowledge\n"
            "</dialectic-pass>"
        )

    def _should_bail_out(self, pass_text: str) -> bool:
        """Bail-out heuristic: skip remaining passes if strong signal.

        Returns True (bail out) when:
        - Empty streak count exceeds threshold (user not engaging)
        - The pass text is empty (something went wrong)

        Returns False (continue to next pass) to perform deeper reasoning.
        """
        if not pass_text:
            return True
        # If user is in a silent streak, bail on deeper passes
        if self._empty_streak >= self._max_empty_streak:
            return True
        return False

    # -- Phase 3: Trivial-prompt filter -------------------------------------

    _TRIVIAL_PATTERNS = [
        "yes", "no", "ok", "okay", "k", "kk", "sure", "yep", "nope",
        "thanks", "ty", "thank you", "thx", "np", "yw",
        "👍", "✅", "🙏", "thanks!",
    ]

    def _is_trivial_prompt(self, prompt: str) -> bool:
        """Check if a user prompt is trivial (yes/no/ok/etc.).

        Trivial prompts don't need context injection because they don't
        reference any stored information. Skipping injection saves
        context window space.

        Returns:
            True if the prompt matches known trivial patterns.
        """
        if not prompt:
            return True
        cleaned = prompt.strip().lower().rstrip(".!?")
        return cleaned in self._TRIVIAL_PATTERNS

    # -- Phase 3: Diagnostics -----------------------------------------------

    def _empty_profile_hint(self) -> str:
        """Return a diagnostic hint when the peer card is empty.

        Called by the agent or operator to understand why context
        injection feels thin. Returns an actionable message.
        """
        if not self._available:
            return (
                "Ragamuffin provider is not available. "
                "Check RAGAMUFFIN_ENDPOINT and that the service is running."
            )
        card = self._get_peer_card()
        if card:
            return (
                f"Peer card is set ({len(card)} chars). "
                "If context still feels thin, check:\n"
                "- _context_cache_turn vs current turn\n"
                "- RAGAMUFFIN_CONTEXT_CADENCE (default 3)\n"
                "- recall_mode (should not be 'tools')"
            )
        return (
            "Peer card is empty. Use `ragamuffin_profile` with a 'value' "
            "to describe this agent's role and knowledge. Example:\n"
            "ragamuffin_profile(value=\"Dev agent - builds software, "
            "maintains code, works from GitHub Issues\")"
        )

    def _liveness_snapshot(self) -> Dict[str, Any]:
        """Return a debug snapshot of the provider's internal state.

        Useful for diagnosing injection behavior, cadence issues,
        and connectivity problems during development.

        Returns:
            Dict with config, state, and timing info.
        """
        effective_dialectic = (
            self._dialectic_cadence
            if self._empty_streak < self._max_empty_streak
            else self._dialectic_cadence * 2
        )
        return {
            "config": {
                "endpoint": self._endpoint,
                "vault": self._agent_vault,
                "agent_identity": self._agent_identity,
                "recall_mode": self._recall_mode,
                "save_messages": self._save_messages,
                "injection_frequency": self._injection_frequency,
                "context_cadence": self._context_cadence,
                "dialectic_cadence": self._dialectic_cadence,
                "dialectic_depth": self._dialectic_depth,
                "empty_streak_backoff": self._empty_streak_backoff,
                "session_id": self._session_id,
            },
            "state": {
                "available": self._available,
                "vault_ready": self._vault_ready,
                "turn_counter": self._turn_counter,
                "empty_streak": self._empty_streak,
                "context_cache_turn": self._context_cache_turn,
                "dialectic_cache_turn": self._dialectic_cache_turn,
                "effective_dialectic_cadence": effective_dialectic,
            },
            "cache": {
                "base_context_length": len(self._base_context_cache),
                "pending_dialectic_length": len(self._pending_dialectic),
                "prefetch_result_length": len(self._prefetch_result),
            },
            "peer_card": self._get_peer_card()[:200] if self._get_peer_card() else None,
        }

    def _wrap_context(self, context_str: str) -> str:
        """Wrap context in <memory-context> XML fences.

        Returns empty string if context is empty or if recall_mode
        is 'tools' (no auto-injection).

        Includes a refresh marker (#785) so the agent can determine
        the staleness of the injected context.
        """
        if not context_str or self._recall_mode == "tools":
            return ""
        marker = (
            f"<!-- memory-context refreshed at turn {self._turn_counter} "
            f"({time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}) -->\n"
        )
        return marker + f"<memory-context>\n{context_str}\n</memory-context>"

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        """Return cached layered context from _wrap_context().

        Waits briefly for the background thread if still running,
        then builds layered context from:
        1. _base_context_cache (refreshed on context_cadence)
        2. _pending_dialectic (refreshed on dialectic_cadence)
        3. Prefetch recall results

        Phase 3: If query is a trivial prompt, returns empty to avoid
        wasting context window space on yes/no responses.

        Returns empty string when nothing is cached, to avoid emitting
        empty <memory-context> fences.
        """
        # Phase 3 — skip injection for trivial prompts
        if self._is_trivial_prompt(query):
            return ""

        if self._prefetch_thread and self._prefetch_thread.is_alive():
            self._prefetch_thread.join(timeout=_PREFETCH_TIMEOUT)

        with self._prefetch_lock:
            result = self._prefetch_result
            self._prefetch_result = ""

        layers = []
        if self._base_context_cache:
            layers.append(self._base_context_cache)
        if self._pending_dialectic:
            layers.append(self._pending_dialectic)
        if result:
            layers.append(f"## Recent Recall\n{result}")

        if not layers:
            return ""
        return self._wrap_context("\n\n".join(layers))

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        """Queue a background recall for the NEXT turn.

        Phase 2: In 'context' recall_mode, prefetch is skipped entirely
        (tools are hidden, no recall injection needed). Also respects
        first_turn injection frequency — only fetches on the first turn.
        """
        if not self._available or not self._vault_ready or not query:
            return

        # Phase 2 — skip prefetch in 'context' mode (no recall tools)
        if self._recall_mode == "context":
            return

        # Phase 2 — first_turn injection: only prefetch on turn 0
        if (
            self._injection_frequency == "first_turn"
            and self._turn_counter > 0
        ):
            return

        def _run():
            try:
                if self._requests is None:
                    return
                url = _build_endpoint(
                    self._endpoint, f"/vault/{self._agent_vault}/recall"
                )
                headers = _build_headers(self._auth_token)
                payload = {
                    "query": query,
                    "top_k": 5,
                    "score_threshold": 0.3,
                }

                resp = self._requests.post(
                    url, json=payload, headers=headers, timeout=_REQUEST_TIMEOUT
                )
                if resp.status_code == 200:
                    data = resp.json()
                    results = data.get("results", [])
                    if results:
                        lines = []
                        for r in results:
                            text = r.get("text", "")
                            score = r.get("score", 0)
                            lines.append(f"[score={score:.2f}] {text}")
                        with self._prefetch_lock:
                            self._prefetch_result = "\n\n".join(lines)
            except Exception as e:
                logger.debug("Ragamuffin prefetch error: %s", e)

        self._prefetch_thread = threading.Thread(
            target=_run, daemon=True, name="ragamuffin-prefetch"
        )
        self._prefetch_thread.start()

    def sync_turn(
        self,
        user_content: str,
        assistant_content: str,
        *,
        session_id: str = "",
    ) -> None:
        """Persist a completed turn asynchronously.

        Phase 2: Also refreshes base context and dialectic caches
        on cadence (every context_cadence / dialectic_cadence turns).
        Skips persistence when save_messages is False.
        """
        if not self._available or not self._vault_ready:
            return

        self._turn_counter += 1

        # Phase 3 — empty streak tracking
        if self._is_trivial_prompt(user_content):
            self._empty_streak += 1
        else:
            self._empty_streak = 0

        # Phase 3 — skip context injection for trivial prompts
        if self._is_trivial_prompt(user_content):
            if not self._save_messages:
                return
            text = f"User: {user_content}\nAssistant: {assistant_content}"
            def _run():
                try:
                    if self._requests is None:
                        return
                    url = _build_endpoint(self._endpoint, "/v1/ingest")
                    headers = _build_headers(self._auth_token)
                    payload = {
                        "vault": self._agent_vault,
                        "content": text,
                        "source": "session_turn",
                        "tags": ["session", self._agent_identity],
                    }
                    self._requests.post(url, json=payload, headers=headers, timeout=_REQUEST_TIMEOUT)
                except Exception as e:
                    logger.debug("Ragamuffin sync_turn error: %s", e)
            self._sync_thread = threading.Thread(target=_run, daemon=True, name="ragamuffin-sync")
            self._sync_thread.start()
            return

        # Phase 2 — cadence-gated context refresh
        if (
            self._context_cadence > 0
            and self._turn_counter - self._context_cache_turn
            >= self._context_cadence
        ):
            self._base_context_cache = self._build_base_context()
            self._context_cache_turn = self._turn_counter
            logger.debug(
                "Context cache refreshed at turn %d",
                self._turn_counter,
            )

        # Phase 3 — empty-streak backoff: double cadence on silent streaks
        effective_dialectic_cadence = self._dialectic_cadence
        if (
            self._empty_streak_backoff
            and self._empty_streak >= self._max_empty_streak
        ):
            effective_dialectic_cadence = self._dialectic_cadence * 2

        if (
            self._dialectic_cadence > 0
            and self._turn_counter - self._dialectic_cache_turn
            >= effective_dialectic_cadence
        ):
            self._pending_dialectic = self._build_dialectic()
            self._dialectic_cache_turn = self._turn_counter
            logger.debug(
                "Dialectic cache refreshed at turn %d (cadence=%d)",
                self._turn_counter,
                effective_dialectic_cadence,
            )

        # Phase 2 — skip persistence when save_messages is False
        if not self._save_messages:
            return

        text = f"User: {user_content}\nAssistant: {assistant_content}"

        def _run():
            try:
                if self._requests is None:
                    return
                url = _build_endpoint(self._endpoint, "/v1/ingest")
                headers = _build_headers(self._auth_token)
                payload = {
                    "vault": self._agent_vault,
                    "content": text,
                    "source": "session_turn",
                    "tags": ["session", self._agent_identity],
                }
                self._requests.post(
                    url,
                    json=payload,
                    headers=headers,
                    timeout=_REQUEST_TIMEOUT,
                )
            except Exception as e:
                logger.debug("Ragamuffin sync_turn error: %s", e)

        self._sync_thread = threading.Thread(
            target=_run, daemon=True, name="ragamuffin-sync"
        )
        self._sync_thread.start()

    def on_session_end(self, messages: List[Dict[str, Any]]) -> None:
        """Persist session knowledge at session end (issue #793).

        Two things happen automatically, with no agent action required:

        1. A synthesized session summary is indexed via POST /v1/ingest
           (existing behavior) for long-term retrieval.

        2. Key decisions, conclusions, config facts, and preferences are
           extracted from the transcript and written as deduplicated facts
           via POST /v1/facts. Fact keys are deterministic
           (``<prefix>/<domain>/<topic>``), so a later session that reaches
           the same conclusion overwrites the earlier value instead of
           creating a duplicate.

        Network I/O runs in a daemon thread so session shutdown is never
        blocked.
        """
        if not self._available or not self._vault_ready or not messages:
            return

        # Build a summary from the conversation
        total_turns = len(messages) // 2  # rough: user + asst pairs
        first_user_msg = ""
        for m in messages:
            if isinstance(m, dict) and m.get("role") == "user":
                first_user_msg = m.get("content", "")[:200]
                break

        # Extract key topics mentioned across the conversation
        topics = []
        topic_keywords = [
            "decision",
            "question",
            "task",
            "issue",
            "problem",
            "bug",
            "feature",
            "design",
            "PR",
            "merge",
            "deploy",
        ]
        seen_topics = set()
        for m in messages:
            if isinstance(m, dict):
                content = m.get("content", "")
                for keyword in topic_keywords:
                    if keyword in content.lower() and keyword not in seen_topics:
                        topics.append(keyword)
                        seen_topics.add(keyword)

        summary_text = (
            f"Session Summary ({self._session_id})\n"
            f"Agent: {self._agent_identity}\n"
            f"Total turns: {total_turns}\n"
            f"First topic: {first_user_msg}\n"
        )
        if topics:
            summary_text += f"Key topics: {', '.join(topics)}\n"

        doc_id = f"{self._session_id or 'session'}-summary"

        # Extract durable facts from the transcript (issue #793)
        facts = self._extract_session_facts(messages) if self._auto_session_facts else []

        def _run():
            try:
                if self._requests is None:
                    return
                # 1) session summary
                url = _build_endpoint(self._endpoint, "/v1/ingest")
                headers = _build_headers(self._auth_token)
                payload = {
                    "vault": self._agent_vault,
                    "content": summary_text,
                    "source": "session_summary",
                    "tags": [self._session_id or "session", self._agent_identity],
                }
                self._requests.post(
                    url,
                    json=payload,
                    headers=headers,
                    timeout=_REQUEST_TIMEOUT,
                )
                logger.debug("Ragamuffin session summary indexed: %s", doc_id)

                # 2) auto-extracted durable facts (deduplicated by key)
                if facts:
                    stored = 0
                    for fact in facts:
                        if self._put_session_fact(
                            fact["key"], fact["value"], fact["domain"]
                        ):
                            stored += 1
                    logger.info(
                        "Ragamuffin auto-stored %d session fact(s) "
                        "for agent '%s'",
                        stored,
                        self._agent_identity,
                    )
            except Exception as e:  # pragma: no cover - network boundary
                logger.debug("Ragamuffin on_session_end error: %s", e)

        self._sync_thread = threading.Thread(
            target=_run, daemon=True, name="ragamuffin-session-end"
        )
        self._sync_thread.start()

    # -- Issue #793: auto session-to-fact extraction ------------------------

    # Trigger phrases that mark a span as a durable decision / conclusion /
    # config / preference worth persisting as a fact.
    _FACT_TRIGGERS = [
        (r"\bwe (?:decided|agreed|concluded|will use|chose)\b", "decision"),
        (r"\bdecision\b", "decision"),
        (r"\bagreed\b", "decision"),
        (r"\bconclusion\b", "conclusion"),
        (r"\bthe (?:plan|approach|strategy) (?:is|will be)\b", "approach"),
        (r"\bplan\b", "approach"),
        (r"\bconfig(?:ure|uration)?\b", "config"),
        (r"\bset (?:the )?.+? to\b", "config"),
        (r"\benv(?:ironment)? var(?:iable)?\b", "config"),
        (r"\bprefer(?:ence)?\b", "preference"),
        (r"\bstandard\b", "preference"),
        (r"\bshould (?:always )?use\b", "preference"),
        (r"\balways use\b", "preference"),
    ]

    def _extract_session_facts(
        self, messages: List[Dict[str, Any]]
    ) -> List[Dict[str, str]]:
        """Extract durable facts from a transcript.

        Scans assistant (and user) messages for sentences that assert a
        decision, conclusion, config, or preference. Returns a list of
        dicts ``{"key", "value", "domain"}`` with deterministic keys so the
        same conclusion reached in a later session overwrites (dedupes)
        rather than duplicates.

        Only the first occurrence of a given key is kept.
        """
        import hashlib

        # Collect candidate text — assistant conclusions first, then user.
        spans: List[str] = []
        for m in messages:
            if not isinstance(m, dict):
                continue
            role = m.get("role", "")
            content = m.get("content", "")
            if isinstance(content, str) and content.strip():
                spans.append(content)
            elif isinstance(content, list):
                # tool_result / multimodal content blocks
                for block in content:
                    if isinstance(block, dict) and isinstance(
                        block.get("text"), str
                    ):
                        spans.append(block["text"])

        results: List[Dict[str, str]] = []
        seen_keys: set = set()

        for span in spans:
            for sentence in self._split_sentences(span):
                domain = self._classify_fact(sentence)
                if domain is None:
                    continue
                value = sentence.strip()
                if len(value) > 500:
                    value = value[:497] + "..."
                key = self._fact_key(domain, value)
                if key in seen_keys:
                    continue
                seen_keys.add(key)
                results.append({"key": key, "value": value, "domain": domain})

        return results

    @staticmethod
    def _split_sentences(text: str) -> List[str]:
        """Split text into sentences on '.', '!', '?' boundaries."""
        parts = re.split(r"(?<=[.!?])\s+|\n+", text)
        return [p.strip() for p in parts if p and p.strip()]

    def _classify_fact(self, sentence: str) -> Optional[str]:
        """Return the fact domain if the sentence is a durable fact, else None."""
        low = sentence.lower()
        # Must be a substantive assertion, not a question or trivial prompt.
        if "?" in sentence:
            return None
        if len(sentence.split()) < 4:
            return None
        for pattern, domain in self._FACT_TRIGGERS:
            if re.search(pattern, low):
                return domain
        return None

    def _fact_key(self, domain: str, value: str) -> str:
        """Build a deterministic fact key: ``<prefix>/<domain>/<topic>``.

        ``<topic>`` is a short slug from the sentence plus a content hash so
        identical decisions collide on the same key (dedup/overwrite) while
        distinct decisions with overlapping wording stay separate.
        """
        import hashlib

        slug_src = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
        slug = slug_src[:48] or "item"
        digest = hashlib.sha256(value.encode()).hexdigest()[:8]
        return f"{self._session_facts_prefix}/{domain}/{slug}-{digest}"

    def _put_session_fact(self, key: str, value: str, domain: str) -> bool:
        """Upsert a single session fact. POST /v1/facts dedupes by key.

        Returns True on a 200/201, False otherwise (logged at debug level so
        a single bad fact never aborts the rest of the batch).
        """
        if self._requests is None:
            return False
        try:
            url = _build_endpoint(self._endpoint, "/v1/facts")
            headers = _build_headers(self._auth_token)
            payload: Dict[str, Any] = {
                "key": key,
                "value": value,
                "vault": self._agent_vault,
                "tags": ["session_fact", domain, self._agent_identity],
                "source": "session_end_auto",
                "source_type": "conversation",
                "confidence": 0.7,
            }
            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code in (200, 201):
                return True
            logger.debug(
                "Session fact put rejected for %s: HTTP %s %s",
                key,
                resp.status_code,
                resp.text[:200],
            )
            return False
        except Exception as e:  # pragma: no cover - network boundary
            logger.debug("Ragamuffin session fact put error: %s", e)
            return False

    def get_tool_schemas(self) -> List[Dict[str, Any]]:
        """Return tool schemas this provider exposes."""
        return ALL_TOOL_SCHEMAS

    # -- Peer cards --------------------------------------------------------

    def _get_peer_card_key(self) -> str:
        """Return the fact key for this agent's peer card."""
        prefix = _PEER_CARD_PREFIX.format(agent=self._agent_identity)
        return f"{prefix}profile"

    def _get_peer_card(self) -> str:
        """Retrieve this agent's peer card from facts."""
        if not self._requests:
            return ""
        key = self._get_peer_card_key()
        try:
            url = _build_endpoint(self._endpoint, f"/v1/facts/{key}")
            headers = _build_headers(self._auth_token)
            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                data = resp.json()
                return data.get("value", "") or data.get("fact", {}).get(
                    "value", ""
                )
            return ""
        except Exception as e:
            logger.debug("Peer card read error: %s", e)
            return ""

    def _set_peer_card(self, value: str) -> bool:
        """Set this agent's peer card via facts."""
        if not self._requests:
            return False
        key = self._get_peer_card_key()
        try:
            url = _build_endpoint(self._endpoint, "/v1/facts")
            headers = _build_headers(self._auth_token)
            payload = {
                "key": key,
                "value": value,
                "vault": self._agent_vault,
                "tags": ["peer_card", self._agent_identity],
                "source": "ragamuffin_profile",
            }
            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            return resp.status_code in (200, 201)
        except Exception as e:
            logger.debug("Peer card write error: %s", e)
            return False

    # -- Tool dispatch -----------------------------------------------------

    def handle_tool_call(
        self, tool_name: str, args: Dict[str, Any], **kwargs
    ) -> str:
        """Handle a tool call for one of this provider's tools."""
        if tool_name == "ragamuffin_recall":
            return self._handle_recall(args)
        elif tool_name == "ragamuffin_search":
            return self._handle_recall(args)  # alias
        elif tool_name == "ragamuffin_ask":
            return self._handle_ask(args)
        elif tool_name == "ragamuffin_profile":
            return self._handle_profile(args)
        elif tool_name == "ragamuffin_context":
            return self._handle_context(args)
        elif tool_name == "ragamuffin_learn":
            return self._handle_learn(args)
        elif tool_name == "ragamuffin_fact_get":
            return self._handle_fact_get(args)
        elif tool_name == "ragamuffin_fact_put":
            return self._handle_fact_put(args)
        elif tool_name == "ragamuffin_fact_graph":
            return self._handle_fact_graph(args)
        elif tool_name == "ragamuffin_review_list":
            return self._handle_review_list(args)
        elif tool_name == "ragamuffin_review_resolve":
            return self._handle_review_resolve(args)
        elif tool_name == "ragamuffin_status":
            return self._handle_status(args)
        raise NotImplementedError(
            f"Ragamuffin provider does not handle tool '{tool_name}'"
        )

    # ragamuffin_status — health introspection (#784)
    def _handle_status(self, args: Dict[str, Any]) -> str:
        """Return provider and server health status."""
        status = {
            "provider": "ragamuffin",
            "available": self._available,
            "endpoint": self._endpoint,
            "vault": getattr(self, "_agent_vault", ""),
            "recall_mode": getattr(self, "_recall_mode", "hybrid"),
            "tools_injected": [s["name"] for s in ALL_TOOL_SCHEMAS],
            "last_sync_turn": getattr(self, "_turn_counter", 0),
            "context_cache_turn": getattr(self, "_context_cache_turn", 0),
        }
        # Probe server health
        if self._requests and self._endpoint:
            try:
                url = _build_endpoint(self._endpoint, "/health")
                headers = _build_headers(self._auth_token)
                resp = self._requests.get(
                    url, headers=headers, timeout=_REQUEST_TIMEOUT
                )
                if resp.status_code == 200:
                    status["server_health"] = resp.json()
                else:
                    status["server_health"] = {
                        "status": "error",
                        "code": resp.status_code,
                    }
            except Exception as e:
                status["server_health"] = {"status": "unreachable", "error": str(e)}
        else:
            status["server_health"] = {"status": "not_configured"}
        return json.dumps(status, indent=2)

    # -- Tool call dispatch --

    # -- Tool handlers -----------------------------------------------------

    def _handle_recall(self, args: Dict[str, Any]) -> str:
        """POST /vault/{name}/recall against the specified vault."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        vault = args.get("vault", self._agent_vault)
        query = args.get("query", "")
        limit = args.get("limit", 5)
        min_score = args.get("min_score", 0.0)

        if not query:
            return json.dumps({"error": "Query is required"})

        try:
            url = _build_endpoint(self._endpoint, f"/vault/{vault}/recall")
            headers = _build_headers(self._auth_token)
            payload = {
                "query": query,
                "top_k": min(max(limit, 1), 100),
                "score_threshold": min(max(min_score, 0.0), 1.0),
            }

            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code == 200:
                data = resp.json()
                results = data.get("results", [])
                if not results:
                    return json.dumps(
                        {"matches": [], "note": "No relevant results found."}
                    )

                formatted = []
                for r in results:
                    formatted.append(
                        {
                            "text": r.get("text", ""),
                            "score": r.get("score", 0.0),
                            "metadata": r.get("metadata", {}),
                        }
                    )
                return json.dumps({"matches": formatted}, indent=2)
            else:
                return json.dumps(
                    {
                        "error": f"Recall failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin recall error: %s", e)
            return json.dumps({"error": f"Recall failed: {e}"})

    def _handle_ask(self, args: Dict[str, Any]) -> str:
        """POST /ask - synthesis with citations."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        query = args.get("query", "")
        mode = args.get("mode", "standard")
        top_k = args.get("top_k", 5)
        reasoning_effort = args.get("reasoning_effort", "auto")

        if not query:
            return json.dumps({"error": "Query is required"})

        try:
            url = _build_endpoint(self._endpoint, "/ask")
            headers = _build_headers(self._auth_token)
            payload = {
                "query": query,
                "mode": mode,
                "top_k": min(max(top_k, 1), 20),
                "vault": self._agent_vault,
            }
            if reasoning_effort != "auto":
                payload["reasoning_effort"] = reasoning_effort

            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code == 200:
                return json.dumps(resp.json(), indent=2)
            elif resp.status_code == 503:
                return json.dumps(
                    {
                        "error": "ASK_UNAVAILABLE",
                        "detail": "LLM not configured",
                    }
                )
            else:
                return json.dumps(
                    {
                        "error": f"Ask failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin ask error: %s", e)
            return json.dumps({"error": f"Ask failed: {e}"})

    def _handle_profile(self, args: Dict[str, Any]) -> str:
        """Get or update the agent's peer card."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        value = args.get("value", "")

        if value:
            # Write mode
            success = self._set_peer_card(value)
            if success:
                return json.dumps(
                    {
                        "status": "ok",
                        "agent": self._agent_identity,
                        "card": value,
                    },
                    indent=2,
                )
            return json.dumps(
                {
                    "error": "Failed to update peer card",
                    "agent": self._agent_identity,
                }
            )
        else:
            # Read mode
            card = self._get_peer_card()
            if card:
                return json.dumps(
                    {
                        "agent": self._agent_identity,
                        "card": card,
                    },
                    indent=2,
                )
            return json.dumps(
                {
                    "agent": self._agent_identity,
                    "card": None,
                    "note": "No peer card set. Use ragamuffin_profile with a 'value' to create one.",
                },
                indent=2,
            )

    def _handle_context(self, args: Dict[str, Any]) -> str:
        """Get composite context bundle (card + summary + recall)."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        query = args.get("query", "")
        top_k = args.get("top_k", 3)

        bundle = {
            "agent": self._agent_identity,
            "vault": self._agent_vault,
        }

        # Get peer card
        card = self._get_peer_card()
        if card:
            bundle["card"] = card

        # Get session summary (from context_bundle cache or facts prefix search)
        try:
            url = _build_endpoint(
                self._endpoint,
                f"/v1/facts?prefix=session/{self._session_id}",
            )
            headers = _build_headers(self._auth_token)
            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                data = resp.json()
                facts = data.get("facts", []) or data.get("results", [])
                if facts:
                    bundle["session_summary"] = facts[-1].get("value", "")
        except Exception as e:
            logger.debug("Context summary fetch error: %s", e)

        # Get recall results if query provided
        if query:
            try:
                url = _build_endpoint(
                    self._endpoint, f"/vault/{self._agent_vault}/recall"
                )
                headers = _build_headers(self._auth_token)
                payload = {
                    "query": query,
                    "top_k": min(max(top_k, 1), 10),
                    "score_threshold": 0.0,
                }
                resp = self._requests.post(
                    url,
                    json=payload,
                    headers=headers,
                    timeout=_REQUEST_TIMEOUT,
                )
                if resp.status_code == 200:
                    data = resp.json()
                    results = data.get("results", [])
                    if results:
                        bundle["recall"] = [
                            {
                                "text": r.get("text", ""),
                                "score": r.get("score", 0.0),
                            }
                            for r in results
                        ]
            except Exception as e:
                logger.debug("Context recall fetch error: %s", e)

        return json.dumps(bundle, indent=2)

    def _handle_learn(self, args: Dict[str, Any]) -> str:
        """Store a conclusion as a persistent fact."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        statement = args.get("statement", "")
        if not statement:
            return json.dumps({"error": "Statement is required"})

        # Generate a key from the statement
        import hashlib

        key_hash = hashlib.sha256(statement.encode()).hexdigest()[:12]
        key = f"conclusion/{self._session_id or 'session'}/{key_hash}"

        try:
            url = _build_endpoint(self._endpoint, "/v1/facts")
            headers = _build_headers(self._auth_token)
            payload: Dict[str, Any] = {
                "key": key,
                "value": statement,
                "vault": self._agent_vault,
                "tags": ["conclusion", self._agent_identity],
                "source": "ragamuffin_learn",
            }
            if "confidence" in args:
                payload["confidence"] = min(
                    max(float(args["confidence"]), 0.0), 1.0
                )
            if "tags" in args:
                existing_tags = payload.get("tags", [])
                payload["tags"] = existing_tags + list(args["tags"])

            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code in (200, 201):
                result = resp.json()
                return json.dumps(
                    {
                        "status": "ok",
                        "key": key,
                        "statement": statement,
                    },
                    indent=2,
                )
            else:
                return json.dumps(
                    {
                        "error": f"Learn failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin learn error: %s", e)
            return json.dumps({"error": f"Learn failed: {e}"})

    def _handle_fact_get(self, args: Dict[str, Any]) -> str:
        """GET /v1/facts/{key} - retrieve a fact by key."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        key = args.get("key", "")
        if not key:
            return json.dumps({"error": "Key is required"})

        try:
            url = _build_endpoint(self._endpoint, f"/v1/facts/{key}")
            headers = _build_headers(self._auth_token)

            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                return json.dumps(resp.json(), indent=2)
            elif resp.status_code == 404:
                return json.dumps(
                    {
                        "error": "NOT_FOUND",
                        "detail": f"Fact '{key}' not found",
                    }
                )
            else:
                return json.dumps(
                    {
                        "error": f"Fact get failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin fact_get error: %s", e)
            return json.dumps({"error": f"Fact get failed: {e}"})

    def _handle_fact_put(self, args: Dict[str, Any]) -> str:
        """POST /v1/facts - write/upsert a fact."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        key = args.get("key", "")
        value = args.get("value", "")
        if not key or not value:
            return json.dumps(
                {"error": "Both 'key' and 'value' are required"}
            )

        try:
            url = _build_endpoint(self._endpoint, "/v1/facts")
            headers = _build_headers(self._auth_token)
            payload: Dict[str, Any] = {
                "key": key,
                "value": value,
                "vault": self._agent_vault,
            }
            if "confidence" in args:
                payload["confidence"] = min(
                    max(float(args["confidence"]), 0.0), 1.0
                )
            if "ttl_days" in args:
                payload["ttl_days"] = int(args["ttl_days"])
            if "tags" in args:
                payload["tags"] = args["tags"]
            if "source" in args:
                payload["source"] = args["source"]

            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code in (200, 201):
                return json.dumps(resp.json(), indent=2)
            else:
                return json.dumps(
                    {
                        "error": f"Fact put failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin fact_put error: %s", e)
            return json.dumps({"error": f"Fact put failed: {e}"})

    def _handle_fact_graph(self, args: Dict[str, Any]) -> str:
        """GET /v1/facts/{key}/graph - fact lineage."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        key = args.get("key", "")
        if not key:
            return json.dumps({"error": "Key is required"})

        try:
            url = _build_endpoint(self._endpoint, f"/v1/facts/{key}/graph")
            headers = _build_headers(self._auth_token)

            resp = self._requests.get(
                url, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                return json.dumps(resp.json(), indent=2)
            elif resp.status_code == 404:
                return json.dumps(
                    {
                        "error": "NOT_FOUND",
                        "detail": f"Fact '{key}' not found",
                    }
                )
            else:
                return json.dumps(
                    {
                        "error": f"Fact graph failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin fact_graph error: %s", e)
            return json.dumps({"error": f"Fact graph failed: {e}"})

    def _handle_review_list(self, args: Dict[str, Any]) -> str:
        """GET /v1/review - list flagged facts."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        try:
            params = {}
            if "reason" in args:
                params["reason"] = args["reason"]
            if "limit" in args:
                params["limit"] = min(max(int(args["limit"]), 1), 100)

            url = _build_endpoint(self._endpoint, "/v1/review")
            headers = _build_headers(self._auth_token)

            resp = self._requests.get(
                url, params=params, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                return json.dumps(resp.json(), indent=2)
            else:
                return json.dumps(
                    {
                        "error": f"Review list failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin review_list error: %s", e)
            return json.dumps({"error": f"Review list failed: {e}"})

    def _handle_review_resolve(self, args: Dict[str, Any]) -> str:
        """POST /v1/review/{point_id}/resolve - resolve a flagged fact."""
        if not self._requests:
            return json.dumps({"error": "Ragamuffin client not available"})

        point_id = args.get("point_id", "")
        action = args.get("action", "")
        if not point_id or not action:
            return json.dumps(
                {"error": "Both 'point_id' and 'action' are required"}
            )

        if action not in ("confirm", "supersede", "reject"):
            return json.dumps(
                {
                    "error": "INVALID_ACTION",
                    "detail": "Action must be 'confirm', 'supersede', or 'reject'",
                }
            )

        try:
            url = _build_endpoint(
                self._endpoint, f"/v1/review/{point_id}/resolve"
            )
            headers = _build_headers(self._auth_token)
            payload: Dict[str, Any] = {"action": action}
            if "correction" in args and action == "supersede":
                payload["correction"] = args["correction"]

            resp = self._requests.post(
                url,
                json=payload,
                headers=headers,
                timeout=_REQUEST_TIMEOUT,
            )
            if resp.status_code == 200:
                return json.dumps(resp.json(), indent=2)
            else:
                return json.dumps(
                    {
                        "error": f"Review resolve failed: HTTP {resp.status_code}",
                        "detail": resp.text[:500],
                    }
                )
        except Exception as e:
            logger.debug("Ragamuffin review_resolve error: %s", e)
            return json.dumps({"error": f"Review resolve failed: {e}"})

    def shutdown(self) -> None:
        """Clean shutdown - wait for pending syncs."""
        if self._sync_thread and self._sync_thread.is_alive():
            self._sync_thread.join(timeout=2.0)
        if self._prefetch_thread and self._prefetch_thread.is_alive():
            self._prefetch_thread.join(timeout=1.0)
        self._available = False
        self._vault_ready = False
        logger.debug("Ragamuffin provider shut down")
