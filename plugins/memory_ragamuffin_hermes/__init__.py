"""Ragamuffin memory plugin — MemoryProvider for Ragamuffin-backed agent memory.

Provides per-agent Qdrant-isolated memory with automatic session persistence,
cross-agent recall, and semantic search via Ragamuffin's HTTP API.

Config via environment variables (profile-scoped via each profile's .env):
  RAGAMUFFIN_ENDPOINT    — Ragamuffin server URL (default: http://ragamuffin:8080)
  RAGAMUFFIN_AUTH_TOKEN  — API key / JWT for authenticated deployments (optional)
  RAGAMUFFIN_VAULT_PREFIX— Prefix for agent vault names (default: agent::)

Lifecycle:
  initialize()     → POST /v1/vaults (create/confirm vault)
  prefetch(query)  → returns cached result from background thread
  queue_prefetch() → background thread → POST /v1/recall
  sync_turn()      → background thread → POST /v1/ingest
  on_session_end() → POST /v1/ingest with session summary
  handle_tool_call → POST /v1/recall against specified vault

Tool schemas:
  ragamuffin_recall — search any agent's vault (cross-agent recall)
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

_DEFAULT_ENDPOINT = "http://ragamuffin:8080"
_VAULT_PREFIX = "agent::"
_REQUEST_TIMEOUT = 15.0  # seconds
_PREFETCH_TIMEOUT = 3.0  # seconds to wait for background thread

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

ALL_TOOL_SCHEMAS = [RECALL_SCHEMA]


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
        ]

    # -- Core lifecycle -----------------------------------------------------

    def initialize(self, session_id: str, **kwargs) -> None:
        """Create/confirm the agent's vault and warm up.

        Reads config from env vars. Derives vault name from
        agent_identity (kwargs) or session_id.
        """
        self._endpoint = os.environ.get("RAGAMUFFIN_ENDPOINT", _DEFAULT_ENDPOINT)
        self._auth_token = os.environ.get("RAGAMUFFIN_AUTH_TOKEN", "")
        self._vault_prefix = os.environ.get("RAGAMUFFIN_VAULT_PREFIX", _VAULT_PREFIX)
        self._session_id = session_id
        self._turn_counter = 0

        # Honor pre-set _requests (e.g. from tests); otherwise lazy-import
        if self._requests is None:
            self._requests = _get_requests()

        if self._requests is None:
            logger.warning("requests library not installed — Ragamuffin plugin disabled")
            self._available = False
            return

        # Resolve agent identity for vault naming
        agent_identity = kwargs.get("agent_identity", "")
        if not agent_identity:
            # Fallback: use session_id or profile name
            agent_identity = kwargs.get("user_id", "") or session_id or "hermes"
        self._agent_identity = agent_identity
        self._agent_vault = f"{self._vault_prefix}{agent_identity}"
        logger.debug(
            "Ragamuffin initialized for agent '%s' → vault '%s' at %s",
            agent_identity, self._agent_vault, self._endpoint,
        )

        # Provision vault
        self._provision_vault()

    def _provision_vault(self) -> bool:
        """GET /vaults to confirm the agent's vault is configured.

        Vaults are configured statically on the server (config file/env).
        This method does NOT create vaults — it just confirms the vault
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
                    logger.warning(
                        "Ragamuffin vault '%s' not found. Configured vaults: %s",
                        self._agent_vault, vault_names,
                    )
                    return False
            else:
                logger.warning(
                    "Ragamuffin vault check failed: %s %s",
                    resp.status_code, resp.text,
                )
                return False
        except Exception as e:
            logger.warning("Ragamuffin vault check error: %s", e)
            return False

    def system_prompt_block(self) -> str:
        """Return status block for the system prompt if ready."""
        if not self._available or not self._vault_ready:
            return ""
        return (
            "# Ragamuffin Agent Memory\n"
            "Active. All turns are automatically persisted.\n"
            "Use `ragamuffin_recall` to search any agent's vault.\n"
        )

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        """Return prefetched context from the background thread.

        Waits briefly for the background thread if still running,
        then returns cached results.
        """
        if self._prefetch_thread and self._prefetch_thread.is_alive():
            self._prefetch_thread.join(timeout=_PREFETCH_TIMEOUT)

        with self._prefetch_lock:
            result = self._prefetch_result
            self._prefetch_result = ""

        if not result:
            return ""
        return f"## Ragamuffin Recall\n{result}\n"

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        """Queue a background recall for the NEXT turn."""
        if not self._available or not self._vault_ready or not query:
            return

        def _run():
            try:
                if self._requests is None:
                    return
                url = _build_endpoint(self._endpoint, f"/vault/{self._agent_vault}/recall")
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

    def sync_turn(self, user_content: str, assistant_content: str,
                  *, session_id: str = "") -> None:
        """Persist a completed turn asynchronously."""
        if not self._available or not self._vault_ready:
            return

        self._turn_counter += 1
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
                    url, json=payload, headers=headers, timeout=_REQUEST_TIMEOUT
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
        topic_keywords = ["decision", "question", "task", "issue", "problem",
                         "bug", "feature", "design", "PR", "merge", "deploy"]
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
                url, json=payload, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            logger.debug("Ragamuffin session summary indexed: %s", doc_id)
        except Exception as e:
            logger.debug("Ragamuffin on_session_end error: %s", e)

    def get_tool_schemas(self) -> List[Dict[str, Any]]:
        """Return tool schemas this provider exposes."""
        return ALL_TOOL_SCHEMAS

    def handle_tool_call(self, tool_name: str, args: Dict[str, Any],
                         **kwargs) -> str:
        """Handle a tool call for one of this provider's tools."""
        if tool_name == "ragamuffin_recall":
            return self._handle_recall(args)
        raise NotImplementedError(
            f"Ragamuffin provider does not handle tool '{tool_name}'"
        )

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
                url, json=payload, headers=headers, timeout=_REQUEST_TIMEOUT
            )
            if resp.status_code == 200:
                data = resp.json()
                results = data.get("results", [])
                if not results:
                    return json.dumps({"matches": [], "note": "No relevant results found."})

                formatted = []
                for r in results:
                    formatted.append({
                        "text": r.get("text", ""),
                        "score": r.get("score", 0.0),
                        "metadata": r.get("metadata", {}),
                    })
                return json.dumps({"matches": formatted}, indent=2)
            else:
                return json.dumps({
                    "error": f"Recall failed: HTTP {resp.status_code}",
                    "detail": resp.text[:500],
                })
        except Exception as e:
            logger.debug("Ragamuffin recall error: %s", e)
            return json.dumps({"error": f"Recall failed: {e}"})

    def shutdown(self) -> None:
        """Clean shutdown — wait for pending syncs."""
        if self._sync_thread and self._sync_thread.is_alive():
            self._sync_thread.join(timeout=2.0)
        if self._prefetch_thread and self._prefetch_thread.is_alive():
            self._prefetch_thread.join(timeout=1.0)
        self._available = False
        self._vault_ready = False
        logger.debug("Ragamuffin provider shut down")
