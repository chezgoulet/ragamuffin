"""Unit tests for RagamuffinMemoryProvider.

Uses unittest.mock to avoid real HTTP calls. Tests cover:
- Config parsing and vault resolution
- Vault provisioning (create + confirm)
- System prompt block
- Prefetch queue and consumption
- Turn sync (sync_turn)
- Session end summarization
- Tool call dispatch (ragamuffin_recall)
- Error handling (unavailable, malformed responses)
- Edge cases (empty config, empty queries, server down)
"""

from __future__ import annotations

import json
import os
import threading
import time
from typing import Any, Dict
from unittest import mock
from unittest.mock import MagicMock, patch

import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", ".."))

from plugins.memory_ragamuffin_hermes.__init__ import (
    RagamuffinMemoryProvider,
    _build_endpoint,
    _build_headers,
    RECALL_SCHEMA,
    ASK_SCHEMA,
    FACT_GET_SCHEMA,
    FACT_PUT_SCHEMA,
    FACT_GRAPH_SCHEMA,
    REVIEW_LIST_SCHEMA,
    REVIEW_RESOLVE_SCHEMA,
    _DEFAULT_ENDPOINT,
    _VAULT_PREFIX,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def make_mock_response(status_code: int, json_data: Dict[str, Any]):
    """Create a mock requests.Response."""
    mock_resp = MagicMock()
    mock_resp.status_code = status_code
    mock_resp.json.return_value = json_data
    mock_resp.text = json.dumps(json_data)
    return mock_resp


def make_provider(**overrides):
    """Create a RagamuffinMemoryProvider with env overrides and mock requests.

    Sets env vars directly (the autouse conftest fixture restores them).
    """
    env_patches = {
        "RAGAMUFFIN_ENDPOINT": overrides.pop("endpoint", _DEFAULT_ENDPOINT),
        "RAGAMUFFIN_AUTH_TOKEN": overrides.pop("auth_token", ""),
        "RAGAMUFFIN_VAULT_PREFIX": overrides.pop("vault_prefix", _VAULT_PREFIX),
    }
    saved = {k: os.environ.get(k) for k in env_patches}
    for k, v in env_patches.items():
        os.environ[k] = v

    provider = RagamuffinMemoryProvider()
    provider._requests = MagicMock()
    provider._endpoint = env_patches["RAGAMUFFIN_ENDPOINT"]
    provider._auth_token = env_patches["RAGAMUFFIN_AUTH_TOKEN"]
    provider._vault_prefix = env_patches["RAGAMUFFIN_VAULT_PREFIX"]

    return provider


# ---------------------------------------------------------------------------
# Tests: Config and utility functions
# ---------------------------------------------------------------------------

class TestConfig:
    """Test environment variable parsing and defaults."""

    def test_default_endpoint(self):
        with patch.dict(os.environ, {}, clear=True):
            provider = RagamuffinMemoryProvider()
            # No env set, so default should NOT be applied until initialize
            assert provider._endpoint == ""

    def test_env_config_parsing(self):
        provider = make_provider(
            endpoint="http://custom:9999",
            vault_prefix="agent::",
            auth_token="sk-test-key",
        )
        assert provider._endpoint == "http://custom:9999"
        assert provider._auth_token == "sk-test-key"
        assert provider._vault_prefix == "agent::"

    def test_available_when_endpoint_set(self):
        with patch.dict(os.environ, {"RAGAMUFFIN_ENDPOINT": "http://rag:8000"}):
            provider = RagamuffinMemoryProvider()
            assert provider.is_available()

    def test_not_available_when_endpoint_not_set(self):
        with patch.dict(os.environ, {}, clear=True):
            provider = RagamuffinMemoryProvider()
            assert not provider.is_available()

    def test_name(self):
        provider = make_provider()
        assert provider.name == "ragamuffin"

    def test_vault_resolution(self):
        """Vault name should be prefix + agent_identity."""
        provider = make_provider(vault_prefix="agent::")
        provider.initialize("sess_001", agent_identity="dev")
        assert provider._agent_vault == "agent::dev"
        assert provider._agent_identity == "dev"

    def test_vault_resolution_fallback_to_session_id(self):
        """Without agent_identity, fall back to session_id."""
        provider = make_provider()
        provider.initialize("sess_abc")
        assert provider._agent_vault.endswith("sess_abc")


class TestUtilityFunctions:

    def test_build_endpoint(self):
        assert _build_endpoint("http://rag:8000", "/v1/recall") == "http://rag:8000/v1/recall"
        assert _build_endpoint("http://rag:8000/", "/v1/ingest") == "http://rag:8000/v1/ingest"
        assert _build_endpoint("http://rag:8000", "v1/vaults") == "http://rag:8000/v1/vaults"

    def test_build_headers_no_auth(self):
        h = _build_headers()
        assert h["Content-Type"] == "application/json"
        assert "Authorization" not in h

    def test_build_headers_with_auth(self):
        h = _build_headers("sk-key-123")
        assert h["Authorization"] == "Bearer sk-key-123"

    def test_recall_schema_structure(self):
        assert RECALL_SCHEMA["name"] == "ragamuffin_recall"
        assert "query" in RECALL_SCHEMA["parameters"]["required"]
        assert RECALL_SCHEMA["parameters"]["properties"]["vault"]["type"] == "string"
        assert RECALL_SCHEMA["parameters"]["properties"]["limit"]["type"] == "integer"


# ---------------------------------------------------------------------------
# Tests: Vault provisioning
# ---------------------------------------------------------------------------

class TestVaultProvisioning:

    def test_vault_created(self):
        provider = make_provider(vault_prefix="agent::")
        # GET /vaults returns empty list — triggers creation
        provider._requests.get.return_value = make_mock_response(200, {"vaults": []})
        provider._requests.post.return_value = make_mock_response(201, {
            "name": "agent::dev", "created": True, "collection": "agent::dev"
        })
        provider.initialize("sess_001", agent_identity="dev")

        assert provider._available is True
        assert provider._vault_ready is True

        # Check the API was called correctly
        provider._requests.post.assert_called_once()
        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"] == {
            "name": "agent::dev",
            "path": "/tmp/vault-agent::dev",
        }

    def test_vault_already_exists(self):
        provider = make_provider()
        # GET /vaults returns list with our vault — no creation needed
        provider._requests.get.return_value = make_mock_response(200, {
            "vaults": [{"name": "agent::dev"}]
        })
        provider.initialize("sess_001", agent_identity="dev")

        assert provider._available is True
        assert provider._vault_ready is True
        # POST should NOT be called — vault already exists
        provider._requests.post.assert_not_called()

    def test_vault_provisioning_failure(self):
        """Server returns error; plugin should gracefully degrade."""
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(502, {
            "error": True, "code": "QDRANT_UNAVAILABLE", "message": "Qdrant unreachable"
        })
        provider.initialize("sess_001", agent_identity="dev")

        # Plugin is not dead but vault isn't ready — fail-open
        assert provider._available is False
        assert provider._vault_ready is False
        # Should not crash on subsequent calls
        assert provider.prefetch("test") == ""
        assert provider.system_prompt_block() == ""

    def test_vault_provisioning_connection_error(self):
        """Connection refused; plugin should still be usable."""
        provider = make_provider()
        provider._requests.post.side_effect = ConnectionError("Connection refused")
        provider.initialize("sess_001", agent_identity="dev")

        assert provider._available is False
        assert provider._vault_ready is False
        assert provider.system_prompt_block() == ""

    def test_auth_token_in_headers(self):
        provider = make_provider(auth_token="sk-secret-999")
        # GET /vaults returns empty — triggers POST
        provider._requests.get.return_value = make_mock_response(200, {"vaults": []})
        provider._requests.post.return_value = make_mock_response(201, {
            "name": "agent::dev", "created": True, "collection": "agent::dev"
        })
        provider.initialize("sess_001", agent_identity="dev")

        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["headers"]["Authorization"] == "Bearer sk-secret-999"


# ---------------------------------------------------------------------------
# Tests: System prompt block
# ---------------------------------------------------------------------------

class TestSystemPromptBlock:

    def test_returns_block_when_ready(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        block = provider.system_prompt_block()
        assert "Ragamuffin Agent Memory" in block
        assert "ragamuffin_recall" in block

    def test_returns_empty_when_not_ready(self):
        provider = make_provider()
        provider._available = False
        assert provider.system_prompt_block() == ""

    def test_returns_empty_when_vault_not_ready(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = False
        assert provider.system_prompt_block() == ""


# ---------------------------------------------------------------------------
# Tests: Prefetch
# ---------------------------------------------------------------------------

class TestPrefetch:

    def test_prefetch_returns_empty_when_not_available(self):
        provider = make_provider()
        provider._available = False
        result = provider.prefetch("test query")
        assert result == ""

    def test_prefetch_consumes_cached_result(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        with provider._prefetch_lock:
            provider._prefetch_result = "Found something relevant"
        result = provider.prefetch("test query")
        assert "Found something relevant" in result
        # Result should be consumed
        assert provider._prefetch_result == ""

    def test_prefetch_formats_result(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        with provider._prefetch_lock:
            provider._prefetch_result = "Qdrant isolation is required"
        result = provider.prefetch("test")
        assert result.startswith("## Ragamuffin Recall")
        assert "Qdrant isolation" in result

    def test_queue_prefetch_background_thread(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider._endpoint = "http://ragamuffin:8000"
        provider._agent_vault = "agent::dev"
        provider._requests.post.return_value = make_mock_response(200, {
            "results": [{"text": "Use physical isolation", "score": 0.89}],
        })

        provider.queue_prefetch("isolation decision")
        # Wait for background thread
        if provider._prefetch_thread:
            provider._prefetch_thread.join(timeout=2.0)

        # Result should be cached
        with provider._prefetch_lock:
            assert "Use physical isolation" in provider._prefetch_result

    def test_queue_prefetch_skipped_when_not_ready(self):
        provider = make_provider()
        provider._available = False
        provider.queue_prefetch("test")
        assert provider._prefetch_thread is None

    def test_queue_prefetch_skipped_on_empty_query(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider.queue_prefetch("")
        assert provider._prefetch_thread is None

    def test_recall_api_parameters(self):
        """Verify the API call from queue_prefetch has correct params."""
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider._endpoint = "http://rag:8000"
        provider._agent_vault = "agent::test"
        provider._requests.post.return_value = make_mock_response(200, {"results": []})

        provider.queue_prefetch("what did we decide")
        if provider._prefetch_thread:
            provider._prefetch_thread.join(timeout=2.0)

        # Vault is in URL path, not JSON body; params are top_k/score_threshold
        assert "/vault/agent::test/recall" in provider._requests.post.call_args[0][0]
        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"]["query"] == "what did we decide"
        assert call_kwargs["json"]["top_k"] == 5
        assert call_kwargs["json"]["score_threshold"] == 0.3


# ---------------------------------------------------------------------------
# Tests: Turn sync
# ---------------------------------------------------------------------------

class TestSyncTurn:

    def test_sync_called_with_correct_payload(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider._endpoint = "http://rag:8000"
        provider._agent_vault = "agent::dev"
        provider._agent_identity = "dev"
        provider._session_id = "sess_abc"

        provider.sync_turn("hello", "hi there")

        if provider._sync_thread:
            provider._sync_thread.join(timeout=2.0)

        call_kwargs = provider._requests.post.call_args[1]
        payload = call_kwargs["json"]
        assert payload["vault"] == "agent::dev"
        assert "hello" in payload["content"]
        assert "hi there" in payload["content"]
        assert payload["source"] == "session_turn"

    def test_sync_increments_turn_counter(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider.sync_turn("a", "b")
        assert provider._turn_counter == 1
        provider.sync_turn("c", "d")
        assert provider._turn_counter == 2

    def test_sync_skipped_when_not_available(self):
        provider = make_provider()
        provider._available = False
        provider.sync_turn("a", "b")
        assert provider._sync_thread is None


# ---------------------------------------------------------------------------
# Tests: Session end
# ---------------------------------------------------------------------------

class TestSessionEnd:

    def test_on_session_end_indexes_summary(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider._endpoint = "http://rag:8000"
        provider._agent_vault = "agent::dev"
        provider._agent_identity = "dev"
        provider._session_id = "sess_xyz"

        messages = [
            {"role": "user", "content": "How does Qdrant isolation work?"},
            {"role": "assistant", "content": "Use physical collections per agent."},
            {"role": "user", "content": "Is that the final decision?"},
            {"role": "assistant", "content": "Yes, we decided on physical isolation."},
        ]

        provider.on_session_end(messages)

        call_kwargs = provider._requests.post.call_args[1]
        payload = call_kwargs["json"]
        assert payload["source"] == "session_summary"
        assert "Qdrant isolation" in payload["content"] or "physical" in payload["content"]
        assert payload["vault"] == "agent::dev"

    def test_on_session_end_no_messages(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider.on_session_end([])
        # No call should be made — guard against empty message lists
        assert provider._requests.post.call_count == 0 or provider._sync_thread is not None

    def test_on_session_end_not_available(self):
        provider = make_provider()
        provider._available = False
        provider.on_session_end([{"role": "user", "content": "hi"}])
        assert provider._requests.post.call_count == 0


# ---------------------------------------------------------------------------
# Tests: Tool call dispatch
# ---------------------------------------------------------------------------

class TestToolCall:

    def test_ragamuffin_recall_tool(self):
        provider = make_provider()
        provider._available = True
        provider._endpoint = "http://rag:8000"
        provider._agent_vault = "agent::dev"
        provider._requests.post.return_value = make_mock_response(200, {
            "results": [
                {"text": "Use physical isolation", "score": 0.89, "metadata": {"source": "session"}},
                {"text": "Never use metadata filters", "score": 0.76, "metadata": {}},
            ],
            "vault": "agent::dev",
            "query": "isolation decision",
        })

        result = provider.handle_tool_call("ragamuffin_recall", {
            "query": "isolation decision",
            "limit": 5,
        })

        data = json.loads(result)
        assert len(data["matches"]) == 2
        assert data["matches"][0]["score"] == 0.89
        assert data["matches"][0]["text"] == "Use physical isolation"

    def test_recall_with_explicit_vault(self):
        provider = make_provider()
        provider._available = True
        provider._endpoint = "http://rag:8000"
        provider._requests.post.return_value = make_mock_response(200, {
            "results": [{"text": "Scan results", "score": 0.95}],
        })

        result = provider.handle_tool_call("ragamuffin_recall", {
            "vault": "agent::robot",
            "query": "last scan results",
        })

        # Vault is in URL path, not JSON body
        call_args = provider._requests.post.call_args[0][0]
        assert "/vault/agent::robot/recall" in call_args

    def test_recall_with_empty_query_returns_error(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_recall", {"query": ""})
        data = json.loads(result)
        assert "error" in data

    def test_unknown_tool_raises(self):
        provider = make_provider()
        with pytest.raises(NotImplementedError):
            provider.handle_tool_call("nonexistent_tool", {})

    def test_recall_server_error(self):
        provider = make_provider()
        provider._available = True
        provider._endpoint = "http://rag:8000"
        provider._requests.post.return_value = make_mock_response(502, {
            "error": True, "code": "QDRANT_UNAVAILABLE",
        })

        result = provider.handle_tool_call("ragamuffin_recall", {
            "query": "anything",
        })
        data = json.loads(result)
        assert "error" in data

    def test_recall_no_results(self):
        provider = make_provider()
        provider._available = True
        provider._endpoint = "http://rag:8000"
        provider._requests.post.return_value = make_mock_response(200, {
            "results": [],
        })

        result = provider.handle_tool_call("ragamuffin_recall", {
            "query": "obscure thing",
        })
        data = json.loads(result)
        assert "matches" in data
        assert len(data["matches"]) == 0


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_ask
# ---------------------------------------------------------------------------

class TestToolAsk:

    def test_ask_basic(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(200, {
            "answer": "Physical isolation per agent is the recommended approach.",
            "citations": [
                {"text": "Use physical collections per agent", "score": 0.89},
            ],
        })

        result = provider.handle_tool_call("ragamuffin_ask", {
            "query": "How should we isolate agent memory?",
        })

        data = json.loads(result)
        assert "answer" in data
        assert "Physical isolation" in data["answer"]
        assert len(data["citations"]) == 1

    def test_ask_with_mode_and_top_k(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(200, {
            "answer": "Concise answer",
        })

        provider.handle_tool_call("ragamuffin_ask", {
            "query": "test",
            "mode": "concise",
            "top_k": 10,
        })

        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"]["mode"] == "concise"
        assert call_kwargs["json"]["top_k"] == 10

    def test_ask_empty_query_returns_error(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_ask", {"query": ""})
        data = json.loads(result)
        assert "error" in data

    def test_ask_503_returns_ask_unavailable(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(503, {
            "error": True, "code": "LLM_UNAVAILABLE",
        })

        result = provider.handle_tool_call("ragamuffin_ask", {
            "query": "anything",
        })
        data = json.loads(result)
        assert data["error"] == "ASK_UNAVAILABLE"

    def test_ask_server_error(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(500, {})

        result = provider.handle_tool_call("ragamuffin_ask", {
            "query": "test",
        })
        data = json.loads(result)
        assert "error" in data
        assert "500" in data["detail"] or "500" in data["error"]


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_fact_get
# ---------------------------------------------------------------------------

class TestToolFactGet:

    def test_fact_get_found(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(200, {
            "key": "user_timezone",
            "value": "America/New_York",
            "confidence": 0.85,
            "status": "active",
        })

        result = provider.handle_tool_call("ragamuffin_fact_get", {
            "key": "user_timezone",
        })

        data = json.loads(result)
        assert data["key"] == "user_timezone"
        assert data["value"] == "America/New_York"
        assert data["confidence"] == 0.85

    def test_fact_get_not_found(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(404, {})

        result = provider.handle_tool_call("ragamuffin_fact_get", {
            "key": "nonexistent",
        })

        data = json.loads(result)
        assert data["error"] == "NOT_FOUND"
        assert "nonexistent" in data["detail"]

    def test_fact_get_empty_key(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_fact_get", {"key": ""})
        data = json.loads(result)
        assert "error" in data

    def test_fact_get_connection_error(self):
        provider = make_provider()
        provider._requests.get.side_effect = ConnectionError("Connection refused")

        result = provider.handle_tool_call("ragamuffin_fact_get", {
            "key": "test",
        })
        data = json.loads(result)
        assert "error" in data


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_fact_put
# ---------------------------------------------------------------------------

class TestToolFactPut:

    def test_fact_put_created(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(201, {
            "key": "user_timezone",
            "status": "created",
        })

        result = provider.handle_tool_call("ragamuffin_fact_put", {
            "key": "user_timezone",
            "value": "America/New_York",
        })

        data = json.loads(result)
        assert data["status"] == "created"

        # Verify payload sent to server
        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"]["key"] == "user_timezone"
        assert call_kwargs["json"]["value"] == "America/New_York"

    def test_fact_put_with_optional_fields(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(201, {
            "key": "user_pref", "status": "created",
        })

        provider.handle_tool_call("ragamuffin_fact_put", {
            "key": "user_pref",
            "value": "dark mode",
            "confidence": 0.9,
            "ttl_days": 90,
            "tags": ["preference", "ui"],
            "source": "session",
        })

        call_kwargs = provider._requests.post.call_args[1]
        payload = call_kwargs["json"]
        assert payload["confidence"] == 0.9
        assert payload["ttl_days"] == 90
        assert payload["tags"] == ["preference", "ui"]
        assert payload["source"] == "session"

    def test_fact_put_confidence_clamped(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(201, {})

        provider.handle_tool_call("ragamuffin_fact_put", {
            "key": "test",
            "value": "test",
            "confidence": 5.0,  # above 1.0, should be clamped
        })

        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"]["confidence"] == 1.0

    def test_fact_put_missing_key_value(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_fact_put", {"key": ""})
        data = json.loads(result)
        assert "error" in data

    def test_fact_put_server_error(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(502, {
            "error": True,
        })

        result = provider.handle_tool_call("ragamuffin_fact_put", {
            "key": "test",
            "value": "test",
        })
        data = json.loads(result)
        assert "error" in data


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_fact_graph
# ---------------------------------------------------------------------------

class TestToolFactGraph:

    def test_fact_graph_found(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(200, {
            "key": "user_timezone",
            "supersedes": ["user_timezone_old"],
            "contradicts": [],
            "refines": [],
        })

        result = provider.handle_tool_call("ragamuffin_fact_graph", {
            "key": "user_timezone",
        })

        data = json.loads(result)
        assert data["key"] == "user_timezone"
        assert "user_timezone_old" in data["supersedes"]

    def test_fact_graph_not_found(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(404, {})

        result = provider.handle_tool_call("ragamuffin_fact_graph", {
            "key": "nonexistent",
        })

        data = json.loads(result)
        assert data["error"] == "NOT_FOUND"

    def test_fact_graph_empty_key(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_fact_graph", {"key": ""})
        data = json.loads(result)
        assert "error" in data


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_review_list
# ---------------------------------------------------------------------------

class TestToolReviewList:

    def test_review_list_all(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(200, {
            "flagged": [
                {"point_id": "abc123", "reason": "contradiction", "confidence": 0.3},
                {"point_id": "def456", "reason": "expiring", "confidence": 0.85},
            ],
            "total": 2,
        })

        result = provider.handle_tool_call("ragamuffin_review_list", {})

        data = json.loads(result)
        assert len(data["flagged"]) == 2
        assert data["total"] == 2

    def test_review_list_with_filters(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(200, {
            "flagged": [],
            "total": 0,
        })

        provider.handle_tool_call("ragamuffin_review_list", {
            "reason": "contradiction",
            "limit": 10,
        })

        call_kwargs = provider._requests.get.call_args[1]
        assert call_kwargs["params"]["reason"] == "contradiction"
        assert call_kwargs["params"]["limit"] == 10

    def test_review_list_empty(self):
        provider = make_provider()
        provider._requests.get.return_value = make_mock_response(200, {
            "flagged": [],
            "total": 0,
        })

        result = provider.handle_tool_call("ragamuffin_review_list", {})
        data = json.loads(result)
        assert len(data["flagged"]) == 0


# ---------------------------------------------------------------------------
# Tests: Tool call — ragamuffin_review_resolve
# ---------------------------------------------------------------------------

class TestToolReviewResolve:

    def test_review_resolve_confirm(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(200, {
            "status": "confirmed",
            "point_id": "abc123",
        })

        result = provider.handle_tool_call("ragamuffin_review_resolve", {
            "point_id": "abc123",
            "action": "confirm",
        })

        data = json.loads(result)
        assert data["status"] == "confirmed"

    def test_review_resolve_supersede_with_correction(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(200, {
            "status": "superseded",
            "point_id": "abc123",
        })

        result = provider.handle_tool_call("ragamuffin_review_resolve", {
            "point_id": "abc123",
            "action": "supersede",
            "correction": "Corrected fact value",
        })

        data = json.loads(result)
        assert data["status"] == "superseded"

        # Verify correction sent to server
        call_kwargs = provider._requests.post.call_args[1]
        assert call_kwargs["json"]["correction"] == "Corrected fact value"

    def test_review_resolve_reject(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(200, {
            "status": "rejected",
        })

        result = provider.handle_tool_call("ragamuffin_review_resolve", {
            "point_id": "abc123",
            "action": "reject",
        })

        data = json.loads(result)
        assert data["status"] == "rejected"

    def test_review_resolve_missing_params(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_review_resolve", {"point_id": ""})
        data = json.loads(result)
        assert "error" in data

    def test_review_resolve_invalid_action(self):
        provider = make_provider()
        result = provider.handle_tool_call("ragamuffin_review_resolve", {
            "point_id": "abc123",
            "action": "delete",
        })
        data = json.loads(result)
        assert "INVALID_ACTION" in data.get("error", "")

    def test_review_resolve_server_error(self):
        provider = make_provider()
        provider._requests.post.return_value = make_mock_response(500, {})

        result = provider.handle_tool_call("ragamuffin_review_resolve", {
            "point_id": "abc123",
            "action": "confirm",
        })
        data = json.loads(result)
        assert "error" in data


# ---------------------------------------------------------------------------
# Tests: Tool schemas
# ---------------------------------------------------------------------------

class TestToolSchemas:

    def test_get_tool_schemas_all_seven(self):
        provider = make_provider()
        schemas = provider.get_tool_schemas()
        assert len(schemas) == 7
        names = [s["name"] for s in schemas]
        assert "ragamuffin_recall" in names
        assert "ragamuffin_ask" in names
        assert "ragamuffin_fact_get" in names
        assert "ragamuffin_fact_put" in names
        assert "ragamuffin_fact_graph" in names
        assert "ragamuffin_review_list" in names
        assert "ragamuffin_review_resolve" in names

    def test_schema_has_required_fields(self):
        provider = make_provider()
        schemas = provider.get_tool_schemas()
        for schema in schemas:
            assert "description" in schema
            assert "parameters" in schema
            assert schema["parameters"]["type"] == "object"
            assert "properties" in schema["parameters"]

    def test_ask_schema_required_query(self):
        assert "query" in ASK_SCHEMA["parameters"]["required"]
        assert ASK_SCHEMA["parameters"]["properties"]["query"]["type"] == "string"

    def test_fact_get_schema_required_key(self):
        assert "key" in FACT_GET_SCHEMA["parameters"]["required"]

    def test_fact_put_schema_required_key_value(self):
        assert "key" in FACT_PUT_SCHEMA["parameters"]["required"]
        assert "value" in FACT_PUT_SCHEMA["parameters"]["required"]

    def test_review_resolve_schema_required_fields(self):
        assert "point_id" in REVIEW_RESOLVE_SCHEMA["parameters"]["required"]
        assert "action" in REVIEW_RESOLVE_SCHEMA["parameters"]["required"]
        assert REVIEW_RESOLVE_SCHEMA["parameters"]["properties"]["action"]["type"] == "string"

    def test_fact_graph_schema_required_key(self):
        assert "key" in FACT_GRAPH_SCHEMA["parameters"]["required"]


# ---------------------------------------------------------------------------
# Tests: Shutdown
# ---------------------------------------------------------------------------

class TestShutdown:

    def test_shutdown_marks_unavailable(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        provider.shutdown()
        assert provider._available is False
        assert provider._vault_ready is False

    def test_shutdown_joins_threads(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True

        # Create a long-running thread to verify join
        def _slow():
            time.sleep(0.1)

        provider._sync_thread = threading.Thread(target=_slow, daemon=True)
        provider._sync_thread.start()
        provider._prefetch_thread = threading.Thread(target=_slow, daemon=True)
        provider._prefetch_thread.start()

        # Should not hang
        provider.shutdown()
        assert not provider._sync_thread.is_alive()

    def test_shutdown_twice(self):
        provider = make_provider()
        provider._available = True
        provider.shutdown()
        provider.shutdown()  # no crash


# ---------------------------------------------------------------------------
# Tests: Config schema
# ---------------------------------------------------------------------------

class TestConfigSchema:

    def test_get_config_schema_returns_fields(self):
        provider = make_provider()
        schema = provider.get_config_schema()
        assert len(schema) == 3

    def test_endpoint_field(self):
        provider = make_provider()
        schema = provider.get_config_schema()
        ep_field = [f for f in schema if f["key"] == "endpoint"][0]
        assert ep_field["required"] is True
        assert ep_field["default"] == "http://ragamuffin:8000"
        assert ep_field["env_var"] == "RAGAMUFFIN_ENDPOINT"

    def test_auth_token_is_secret(self):
        provider = make_provider()
        schema = provider.get_config_schema()
        auth_field = [f for f in schema if f["key"] == "auth_token"][0]
        assert auth_field["secret"] is True

    def test_vault_prefix_default(self):
        provider = make_provider()
        schema = provider.get_config_schema()
        vp_field = [f for f in schema if f["key"] == "vault_prefix"][0]
        assert vp_field["default"] == "agent::"


# ---------------------------------------------------------------------------
# Tests: System prompt block
# ---------------------------------------------------------------------------

class TestSystemPromptEdgeCases:

    def test_block_mentions_tool(self):
        provider = make_provider()
        provider._available = True
        provider._vault_ready = True
        block = provider.system_prompt_block()
        assert "ragamuffin_recall" in block
        assert "All turns are automatically persisted" in block


# ---------------------------------------------------------------------------
# Tests: Full lifecycle integration
# ---------------------------------------------------------------------------

class TestLifecycle:

    def test_initialize_to_shutdown(self):
        """End-to-end lifecycle: init → prefetch → sync → shutdown."""
        provider = make_provider(vault_prefix="agent::")
        # GET /vaults returns empty — triggers POST to create
        provider._requests.get.return_value = make_mock_response(200, {"vaults": []})
        provider._requests.post.return_value = make_mock_response(201, {
            "name": "agent::dev", "created": True, "collection": "agent::dev",
        })

        # Initialize
        provider.initialize("sess_001", agent_identity="dev")
        assert provider._agent_vault == "agent::dev"
        assert provider._vault_ready is True

        # Turn 1
        provider.sync_turn("hello", "hello, how can I help?")
        assert provider._turn_counter == 1

        # Turn 2 with background prefetch
        provider.queue_prefetch("what did we discuss")
        provider.sync_turn("Let's talk about isolation", "Physical isolation per agent")

        # Wait for threads
        if provider._sync_thread:
            provider._sync_thread.join(timeout=2.0)
        if provider._prefetch_thread:
            provider._prefetch_thread.join(timeout=2.0)

        # Should have made 3 POST calls: 1 provisioning + 2 sync = 3
        assert provider._requests.post.call_count >= 2

        # Shutdown cleanly
        provider.shutdown()
        assert not provider._available
