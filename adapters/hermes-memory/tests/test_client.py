"""Tests for HTTP client — request building, response parsing, error handling.

The adapter lazily imports ``requests`` and uses ``self._requests.post/self._requests.get``
for all HTTP calls. Our conftest patches ``sys.modules["requests"]`` so every
test gets a mock, controllable via ``fake_requests`` fixture.
"""

import json

import pytest


class TestHealthCheck:
    """``is_available()`` calls GET /health."""

    def test_healthy_server(self, provider, fake_requests):
        fake_requests.get.return_value.status_code = 200
        fake_requests.get.return_value.ok = True
        fake_requests.get.return_value._json_data = {"status": "ok"}

        result = provider.is_available()
        assert result is True
        fake_requests.get.assert_called_once()
        url = fake_requests.get.call_args[0][0]
        assert url.endswith("/health"), f"Expected /health, got {url}"

    def test_unhealthy_server(self, provider, fake_requests):
        fake_requests.get.return_value.status_code = 500
        fake_requests.get.return_value.ok = False

        result = provider.is_available()
        assert result is False

    def test_timeout(self, provider, fake_requests):
        fake_requests.get.side_effect = TimeoutError("Connection timed out")

        result = provider.is_available()
        assert result is False


class TestVaultCreate:
    """``create_vault()`` calls POST /vaults."""

    def test_creates_vault(self, provider, fake_requests):
        fake_requests.post.return_value.status_code = 201
        fake_requests.post.return_value.ok = True
        fake_requests.post.return_value._json_data = {"vault_name": "test::dev"}

        provider.initialize("session-1", agent_identity="dev")
        fake_requests.post.assert_called()
        url, kwargs = fake_requests.post.call_args
        assert url[0].endswith("/vaults")
        assert kwargs["json"]["vault_name"] == "test::dev"
        assert kwargs["json"]["agent_identity"] == "dev"

    def test_auth_token_sent_in_header(self, provider, fake_requests):
        fake_requests.post.return_value.status_code = 201
        fake_requests.post.return_value.ok = True
        fake_requests.post.return_value._json_data = {"vault_name": "test::dev"}

        provider.initialize("session-1", agent_identity="dev")
        _, kwargs = fake_requests.post.call_args
        headers = kwargs.get("headers", {})
        assert headers.get("Authorization") == "Bearer test-token"

    def test_vault_already_exists(self, provider, fake_requests):
        fake_requests.post.return_value.status_code = 409
        fake_requests.post.return_value.ok = False

        # Should not raise — vault exists is not an error
        provider.initialize("session-1", agent_identity="dev")
        assert provider._vault_ready is True


class TestSearchAndRecall:
    """Semantic search calls POST /vaults/{name}/search."""

    def _init_provider(self, provider, fake_requests):
        provider.initialize("session-1", agent_identity="dev")

    def _init_and_prefetch(self, provider, fake_requests):
        self._init_provider(provider, fake_requests)

    def test_search_sends_query(self, provider, fake_requests):
        fake_requests.post.return_value = type("MockResp", (), {
            "status_code": 200, "ok": True,
            "json": lambda: {"results": [{"text": "found it", "score": 0.95}]},
            "text": '{"results": [{"text": "found it", "score": 0.95}]}',
        })()

        provider.initialize("session-1", agent_identity="dev")
        result = provider.recall("what did we discuss?")
        assert "found it" in result

        # Verify search endpoint
        sent_url = fake_requests.post.call_args_list[-1][0][0]
        assert "/search" in sent_url

    def test_search_empty_results(self, provider, fake_requests):
        fake_requests.post.return_value = type("MockResp", (), {
            "status_code": 200, "ok": True,
            "json": lambda: {"results": []},
            "text": '{"results": []}',
        })()

        provider.initialize("session-1", agent_identity="dev")
        result = provider.recall("nothing relevant")
        assert result == "No relevant memories found."

    def test_search_503_error(self, provider, fake_requests):
        fake_requests.post.return_value = type("MockResp", (), {
            "status_code": 503, "ok": False,
            "text": "Service Unavailable",
        })()

        provider.initialize("session-1", agent_identity="dev")
        result = provider.recall("anything")
        assert "error" in result.lower() or "unavailable" in result.lower()

    def test_network_error(self, provider, fake_requests):
        fake_requests.post.side_effect = ConnectionError("Connection refused")

        provider.initialize("session-1", agent_identity="dev")
        result = provider.recall("anything")
        assert "error" in result.lower() or "refused" in result.lower() or "connect" in result.lower()


class TestObserve:
    """``observe()`` calls POST /vaults/{name}/observe."""

    def test_observe_sends_role_and_content(self, provider, fake_requests):
        fake_requests.post.return_value = type("MockResp", (), {
            "status_code": 200, "ok": True,
            "json": lambda: {},
            "text": "{}",
        })()

        provider.initialize("session-1", agent_identity="dev")
        provider.observe("user", "Hello from the user")

        sent_url = fake_requests.post.call_args_list[-1][0][0]
        assert "/observe" in sent_url
        sent_json = fake_requests.post.call_args_list[-1][1]["json"]
        assert sent_json["role"] == "user"
        assert sent_json["content"] == "Hello from the user"
        assert "session_id" in sent_json
