"""Tests for lifecycle — initialize, shutdown, vault creation, sync flow."""

import json
import os
from unittest.mock import patch

import pytest


def _mock_response(status=200, ok=True, json_data=None, text=""):
    return type("MockResp", (), {
        "status_code": status,
        "ok": ok,
        "json": lambda: json_data or {},
        "text": text or (json.dumps(json_data) if json_data else ""),
    })()


class TestInitialize:
    """``initialize()`` sets up vault and session state."""

    def test_initialize_creates_vault_and_sets_available(self, provider, fake_requests):
        """Happy path: vault created, provider available."""
        fake_requests.get.return_value = _mock_response(200, True, {"status": "ok"})
        fake_requests.post.return_value = _mock_response(
            201, True, {"vault_name": "test::dev"}
        )

        provider.initialize("session-1", agent_identity="dev")

        assert provider._vault_ready is True
        assert provider._agent_identity == "dev"
        assert provider._agent_vault == "test::dev"

    def test_initialize_sets_endpoint_from_env(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(200, True, {"status": "ok"})
        fake_requests.post.return_value = _mock_response(201, True, {})

        provider.initialize("session-1", agent_identity="dev")
        assert provider._endpoint == "http://test-ragamuffin:8000"

    def test_initialize_uses_default_endpoint_when_not_set(self, fake_requests, hermes_memory_mod):
        """No env vars — should use default endpoint."""
        mod = hermes_memory_mod
        with patch.dict(os.environ, {}, clear=True):
            prov = mod.RagamuffinMemoryProvider()
            fake_requests.get.return_value = _mock_response(200, True, {"status": "ok"})
            fake_requests.post.return_value = _mock_response(201, True, {})
            prov.initialize("session-1", agent_identity="dev")

        assert prov._endpoint == "http://ragamuffin:8000"

    def test_initialize_with_malformed_env(self, provider, fake_requests):
        """Bad env values should not crash — fall to defaults."""
        fake_requests.get.return_value = _mock_response(200, True, {"status": "ok"})
        fake_requests.post.return_value = _mock_response(201, True, {})

        with patch.dict(os.environ, {
            "RAGAMUFFIN_ENDPOINT": "http://test-ragamuffin:8000",
            "RAGAMUFFIN_RECALL_MODE": "invalid_value!",
        }, clear=True):
            mod = hermes_memory_mod
            prov = mod.RagamuffinMemoryProvider()
            prov.initialize("s1", agent_identity="dev")

        # Should fall back to "hybrid" on invalid value
        assert prov._recall_mode == "hybrid"


class TestIsAvailable:
    """``is_available()`` checks health and auth state."""

    def test_is_available_healthy(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(200, True, {"status": "ok"})
        assert provider.is_available() is True

    def test_is_available_unhealthy(self, provider, fake_requests):
        fake_requests.get.return_value = _mock_response(503, False, {}, "Unavailable")
        assert provider.is_available() is False

    def test_is_available_no_endpoint(self, fake_requests, hermes_memory_mod):
        mod = hermes_memory_mod
        with patch.dict(os.environ, {}, clear=True):
            prov = mod.RagamuffinMemoryProvider()
            prov._endpoint = ""  # Simulate no endpoint configured
            assert prov.is_available() is False


class TestShutdown:
    """``shutdown()`` cleans up state."""

    def test_shutdown_resets_availability(self, provider):
        provider._available = True
        provider._vault_ready = True

        provider.shutdown()

        assert provider._available is False
        assert provider._vault_ready is False

    def test_shutdown_no_threads(self, provider):
        """Shutdown with no active threads should not raise."""
        provider._sync_thread = None
        provider._prefetch_thread = None

        # Should not raise
        provider.shutdown()
        assert True


class TestSaveConfig:
    """``save_config()`` writes Hermes-scoped config."""

    def test_save_config_writes_merged_values(self, provider, tmp_path):
        hermes_home = str(tmp_path)
        provider.save_config({
            "endpoint": "http://custom:8000",
            "vault_prefix": "custom::",
        }, hermes_home)

        config_file = tmp_path / "ragamuffin.json"
        assert config_file.exists()
        data = json.loads(config_file.read_text())
        assert data["endpoint"] == "http://custom:8000"
        assert data["vault_prefix"] == "custom::"

    def test_save_config_merges_with_existing(self, provider, tmp_path):
        hermes_home = str(tmp_path)
        existing = {"endpoint": "http://old:8000", "auth_token": "keep-me"}
        (tmp_path / "ragamuffin.json").write_text(json.dumps(existing))

        provider.save_config({
            "endpoint": "http://new:8000",
            "vault_prefix": "new::",
        }, hermes_home)

        data = json.loads((tmp_path / "ragamuffin.json").read_text())
        assert data["endpoint"] == "http://new:8000"  # overridden
        assert data["auth_token"] == "keep-me"        # preserved
        assert data["vault_prefix"] == "new::"         # added

    def test_save_config_atomic_write(self, provider, tmp_path):
        """Should write via temp+rename, not corrupt on partial write."""
        hermes_home = str(tmp_path)
        provider.save_config({"endpoint": "http://atomic:8000"}, hermes_home)
        # Confirm no .tmp file remains
        leftovers = list(tmp_path.glob("*.tmp"))
        assert len(leftovers) == 0

    def test_save_config_handles_missing_home(self, provider, tmp_path):
        hermes_home = str(tmp_path / "nonexistent")
        # Should not raise
        provider.save_config({"endpoint": "http://new:8000"}, hermes_home)
        config_file = tmp_path / "nonexistent" / "ragamuffin.json"
        assert config_file.exists()
