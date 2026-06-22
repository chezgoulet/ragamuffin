"""Tests for tool dispatch — each tool schema routes to the correct handler.

All 11 tool functions in the provider should:
- Accept arguments matching their schema
- Build and send the correct HTTP request
- Handle empty/error/non-200 responses gracefully
"""

import json

import pytest


# ---------------------------------------------------------------------------
# Helper
# ---------------------------------------------------------------------------


def _mock_response(status=200, ok=True, json_data=None, text=""):
    """Quick mock response factory."""
    return type("MockResp", (), {
        "status_code": status,
        "ok": ok,
        "json": lambda: json_data or {},
        "text": text or (json.dumps(json_data) if json_data else ""),
    })()


# ---------------------------------------------------------------------------
# Tool: ragamuffin_save
# ---------------------------------------------------------------------------


class TestToolSave:
    def test_save_sends_content(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "saved", "memory_id": "mem-1"}
        )
        provider.initialize("s1")
        result = json.loads(provider.handle_tool("ragamuffin_save", {
            "content": "Important fact: Paris is the capital of France",
        }))
        assert result.get("status") == "saved"
        sent = fake_requests.post.call_args_list[-1][1]["json"]
        assert "content" in sent
        assert "session_id" in sent

    def test_save_empty_content(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(400, False, {}, "empty content")
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_save", {"content": ""})
        assert "error" in result.lower() or "empty" in result.lower()


# ---------------------------------------------------------------------------
# Tool: ragamuffin_recall
# ---------------------------------------------------------------------------


class TestToolRecall:
    def test_recall_returns_results(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"results": [{"text": "Paris fact", "score": 0.95}]}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_recall", {
            "query": "capital of France",
        })
        assert "Paris" in result

    def test_recall_empty(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"results": []}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_recall", {"query": "nonexistent"})
        assert "no relevant" in result.lower() or "empty" in result.lower()

    def test_recall_503(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(503, False, {}, "Down")
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_recall", {"query": "test"})
        assert "error" in result.lower() or "503" in result or "unavailable" in result.lower()


# ---------------------------------------------------------------------------
# Tool: ragamuffin_cross_recall
# ---------------------------------------------------------------------------


class TestToolCrossRecall:
    def test_cross_recall_targets_vault(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"results": [{"text": "from robot", "score": 0.85}]}
        )
        provider.initialize("s1", agent_identity="dev")
        result = provider.handle_tool("ragamuffin_cross_recall", {
            "agent_identity": "robot",
            "query": "what does robot know",
        })
        assert "from robot" in result

    def test_cross_recall_self_fallback(self, provider, fake_requests):
        """Should handle querying the same agent gracefully."""
        fake_requests.post.return_value = _mock_response(
            200, True, {"results": [{"text": "local result", "score": 0.9}]}
        )
        provider.initialize("s1", agent_identity="dev")
        result = provider.handle_tool("ragamuffin_cross_recall", {
            "agent_identity": "dev",
            "query": "check my own memory",
        })
        assert "local result" in result


# ---------------------------------------------------------------------------
# Tool: ragamuffin_context / ragamuffin_review
# ---------------------------------------------------------------------------


class TestToolContext:
    def test_context_request(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(
            200, True, {"context": "session summary with key facts"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_context", {"query": "summarize"})
        assert "context" in result or "summary" in result

    def test_context_no_query(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(
            200, True, {"context": "default context snapshot"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_context", {})
        assert "context" in result or "default" in result


# ---------------------------------------------------------------------------
# Tool: ragamuffin_conclude / ragamuffin_profile / ragamuffin_forget
# ---------------------------------------------------------------------------


class TestToolConclude:
    def test_conclude_stores_fact(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "concluded", "conclusion_id": "c1"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_conclude", {
            "conclusion": "User prefers concise answers",
        })
        assert "concluded" in result or "c1" in result


class TestToolProfile:
    def test_profile_read(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(
            200, True, {"card": ["name: User", "role: Developer"]}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_profile", {})
        assert "card" in result or "User" in result

    def test_profile_update(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "updated"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_profile", {
            "card": ["name: Alice", "role: Tester"],
        })
        assert "updated" in result


class TestToolForget:
    def test_forget_respects_memory_id(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "deleted", "memory_id": "mem-42"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_forget", {
            "memory_id": "mem-42",
        })
        assert "deleted" in result or "42" in result


# ---------------------------------------------------------------------------
# Tool: ragamuffin_remember / ragamuffin_amend / ragamuffin_review_page
# ---------------------------------------------------------------------------


class TestToolRemember:
    def test_remember_request(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "remembered", "memory_id": "mem-7"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_remember", {
            "query": "what was decided about X",
        })
        assert "remembered" in result or "mem-7" in result


class TestToolAmend:
    def test_amend_correction(self, provider, fake_requests):
        fake_requests.post.return_value = _mock_response(
            200, True, {"status": "amended", "memory_id": "mem-3"}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_amend", {
            "memory_id": "mem-3",
            "correction": "Actually it was Tuesday, not Wednesday",
        })
        assert "amended" in result or "mem-3" in result

    def test_amend_without_id_returns_error(self, provider, fake_requests):
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_amend", {
            "correction": "wrong memory reference",
        })
        assert "error" in result.lower() or "memory_id" in result.lower()


class TestToolReviewPage:
    def test_review_page_list(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(
            200, True, {"page": 1, "total": 3, "memories": []}
        )
        provider.initialize("s1")
        result = provider.handle_tool("ragamuffin_review_page", {"page": 1})
        assert "page" in result or "memories" in result


# ---------------------------------------------------------------------------
# Unknown tool
# ---------------------------------------------------------------------------


class TestUnknownTool:
    def test_unknown_tool_returns_error(self, provider):
        provider.initialize("s1")
        result = provider.handle_tool("nonexistent_tool", {})
        assert "error" in result.lower() or "unknown" in result.lower()
