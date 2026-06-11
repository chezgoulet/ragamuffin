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
  on_session_end()   → POST /v1/ingest with session summary
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

        # Context bundle cache
        self._context_bundle: Dict[str, Any] = {}

    # -- Identity -----------------------------------------------------------

    @property
    def name(self) -> str:
        return "ragamuffin"

    # -- Availability check -------------------------------------------------

    def is_available(self) -> bool:
        """Return True if Ragamuffin endpoint is configured."""
        return bool(os.environ.get("RAGAMUFFIN_ENDPOINT"))

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
        config_path = os.environ.get("RAGAMUFFIN_CONFIG", "")
        if not config_path:
            hermes_home = os.environ.get("HERMES_HOME", "")
            if hermes_home:
                config_path = os.path.join(hermes_home, "ragamuffin.json")

        if not config_path or not os.path.exists(config_path):
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
        self._context_cache_turn = 0
        self._dialectic_cache_turn = 0

        # Honor pre-set _requests (e.g. from tests); otherwise lazy-import
        if self._requests is None:
            self._requests = _get_requests()

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
        """Build the base context layer: peer card + session summary."""
        if not self._requests:
            return ""
        parts = []

        card = self._get_peer_card()
        if card:
            parts.append(f"--- Peer Card ({self._agent_identity}) ---\n{card}")

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

        return "\n\n".join(parts)

    def _build_dialectic(self) -> str:
        """Build the dialectic reasoning layer (placeholder for Phase 3).

        Phase 3 will extend this with multi-pass reasoning, cold/warm
        prompt selection, bail-out heuristics, and empty-streak backoff.
        Currently returns an empty string (no dialectic without depth).
        """
        return ""

    def _wrap_context(self, context_str: str) -> str:
        """Wrap context in <memory-context> XML fences.

        Returns empty string if context is empty or if recall_mode
        is 'tools' (no auto-injection).
        """
        if not context_str or self._recall_mode == "tools":
            return ""
        return f"<memory-context>\n{context_str}\n</memory-context>"

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        """Return cached layered context from _wrap_context().

        Waits briefly for the background thread if still running,
        then builds layered context from:
        1. _base_context_cache (refreshed on context_cadence)
        2. _pending_dialectic (refreshed on dialectic_cadence)
        3. Prefetch recall results

        Returns empty string instead of _wrap_context() when nothing
        is cached, to avoid emitting empty <memory-context> fences.
        """
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

        if (
            self._dialectic_cadence > 0
            and self._turn_counter - self._dialectic_cache_turn
            >= self._dialectic_cadence
        ):
            self._pending_dialectic = self._build_dialectic()
            self._dialectic_cache_turn = self._turn_counter
            logger.debug(
                "Dialectic cache refreshed at turn %d",
                self._turn_counter,
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
        """Index an end-of-session summary.

        messages is the full conversation history provided by Hermes.
        We synthesize a summary document for long-term retrieval.
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

        try:
            if self._requests is None:
                return
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
        except Exception as e:
            logger.debug("Ragamuffin on_session_end error: %s", e)

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
        raise NotImplementedError(
            f"Ragamuffin provider does not handle tool '{tool_name}'"
        )

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
