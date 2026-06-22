"""Tests for config resolution — env-var parsing, defaults, config file override."""

import json
import os
from pathlib import Path
from unittest.mock import patch

import pytest


class TestDefaults:
    """Default values when nothing is configured."""

    def test_default_endpoint(self, hermes_memory_mod):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        # Not initialized yet — env-vars and config are loaded in initialize()
        assert prov._endpoint == ""  # unset before initialize

    def test_default_vault_prefix(self, hermes_memory_mod):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        assert prov._vault_prefix == "agent::"

    def test_default_recall_mode(self, hermes_memory_mod):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        assert prov._recall_mode == "hybrid"

    def test_default_cadence_values(self, hermes_memory_mod):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        assert prov._context_cadence == 3
        assert prov._dialectic_cadence == 5
        assert prov._dialectic_depth == 1
        assert prov._save_messages is True

    def test_is_available_returns_false_without_env(self, hermes_memory_mod):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        assert prov.is_available() is False

    def test_is_available_returns_true_with_env(self, hermes_memory_mod):
        with patch.dict(os.environ, {"RAGAMUFFIN_ENDPOINT": "http://r:8000"}):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            assert prov.is_available() is True


class TestEnvVarOverride:
    """Environment variables override everything."""

    ENV_VARS = {
        "RAGAMUFFIN_ENDPOINT": "http://env-test:8000",
        "RAGAMUFFIN_AUTH_TOKEN": "env-token-abc",
        "RAGAMUFFIN_VAULT_PREFIX": "env::",
        "RAGAMUFFIN_RECALL_MODE": "context",
        "RAGAMUFFIN_SAVE_MESSAGES": "false",
        "RAGAMUFFIN_INJECTION_FREQ": "first_turn",
        "RAGAMUFFIN_CONTEXT_CADENCE": "10",
        "RAGAMUFFIN_DIALECTIC_CADENCE": "20",
        "RAGAMUFFIN_DIALECTIC_DEPTH": "3",
        "RAGAMUFFIN_EMPTY_STREAK_BACKOFF": "false",
        "RAGAMUFFIN_CROSS_AGENT_VAULTS": "agent::robot,agent::scout",
        "RAGAMUFFIN_FACT_GRAPH_ENABLED": "false",
    }

    def test_env_vars_applied_on_initialize(self, fake_requests, hermes_memory_mod):
        with patch.dict(os.environ, self.ENV_VARS, clear=True):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("test-session", agent_identity="dev")

        assert prov._endpoint == "http://env-test:8000"
        assert prov._auth_token == "env-token-abc"
        assert prov._vault_prefix == "env::"
        assert prov._recall_mode == "context"
        assert prov._save_messages is False
        assert prov._injection_frequency == "first_turn"
        assert prov._context_cadence == 10
        assert prov._dialectic_cadence == 20
        assert prov._dialectic_depth == 3
        assert prov._empty_streak_backoff is False
        assert prov._cross_agent_vaults == ["agent::robot", "agent::scout"]
        assert prov._fact_graph_enabled is False

    def test_vault_name_derived_from_agent_identity(self, fake_requests, hermes_memory_mod):
        with patch.dict(
            os.environ,
            {"RAGAMUFFIN_ENDPOINT": "http://r:8000", "RAGAMUFFIN_VAULT_PREFIX": "env::"},
            clear=True,
        ):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("session-1", agent_identity="dev")
        assert prov._agent_vault == "env::dev"
        assert prov._agent_identity == "dev"

    def test_invalid_recall_mode_falls_back(self, fake_requests, hermes_memory_mod):
        with patch.dict(
            os.environ,
            {"RAGAMUFFIN_ENDPOINT": "http://r:8000", "RAGAMUFFIN_RECALL_MODE": "invalid"},
            clear=True,
        ):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("s")
        assert prov._recall_mode == "hybrid"


class TestConfigFileOverride:
    """Config file ($HERMES_HOME/ragamuffin.json) values apply when env not set."""

    def test_config_file_applied(self, fake_requests, temp_config_file, hermes_memory_mod):
        with patch.dict(
            os.environ,
            {"RAGAMUFFIN_CONFIG": str(temp_config_file)},
            clear=True,
        ):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("session-1", agent_identity="dev")

        assert prov._endpoint == "http://file-config:8000"
        assert prov._vault_prefix == "file::"

    def test_env_var_overrides_config_file(self, fake_requests, temp_config_file, hermes_memory_mod):
        with patch.dict(
            os.environ,
            {
                "RAGAMUFFIN_CONFIG": str(temp_config_file),
                "RAGAMUFFIN_ENDPOINT": "http://env-wins:8000",
            },
            clear=True,
        ):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("session-1", agent_identity="dev")

        # Env var should take precedence over config file
        assert prov._endpoint == "http://env-wins:8000"

    def test_config_file_not_required(self, fake_requests, hermes_memory_mod):
        """Missing config file should not crash initialize()."""
        with patch.dict(
            os.environ,
            {"RAGAMUFFIN_CONFIG": "/nonexistent/path/ragamuffin.json"},
            clear=True,
        ):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("session-1", agent_identity="dev")

        assert prov._endpoint == "http://ragamuffin:8000"  # default
        assert prov._available is False

    def test_no_config_file_no_env(self, fake_requests, hermes_memory_mod):
        """No config file and no env vars — all defaults."""
        with patch.dict(os.environ, {}, clear=True):
            prov = hermes_memory_mod.RagamuffinMemoryProvider()
            prov.initialize("session-1", agent_identity="dev")

        assert prov._endpoint == "http://ragamuffin:8000"
        assert prov._auth_token == ""
        assert prov._vault_prefix == "agent::"
        assert prov._recall_mode == "hybrid"

    def test_malformed_config_file(self, fake_requests, hermes_memory_mod):
        """A malformed config file should not crash initialize()."""
        import tempfile
        tmp = Path(tempfile.mktemp(suffix="_bad_ragamuffin.json"))
        tmp.write_text("not valid json")
        try:
            with patch.dict(
                os.environ,
                {"RAGAMUFFIN_CONFIG": str(tmp)},
                clear=True,
            ):
                prov = hermes_memory_mod.RagamuffinMemoryProvider()
                prov.initialize("session-1", agent_identity="dev")
            # Should fall through to defaults
            assert prov._endpoint == "http://ragamuffin:8000"
        finally:
            tmp.unlink(missing_ok=True)

    def test_hermes_home_config_file(self, fake_requests, hermes_memory_mod):
        """$HERMES_HOME/ragamuffin.json is discovered automatically."""
        import tempfile
        tmp_dir = Path(tempfile.mkdtemp(suffix="_hermes_home"))
        config = tmp_dir / "ragamuffin.json"
        config.write_text(json.dumps({"endpoint": "http://hermes-home:8000"}))

        try:
            with patch.dict(
                os.environ,
                {"HERMES_HOME": str(tmp_dir)},
                clear=True,
            ):
                prov = hermes_memory_mod.RagamuffinMemoryProvider()
                prov.initialize("session-1", agent_identity="dev")
            assert prov._endpoint == "http://hermes-home:8000"
        finally:
            import shutil
            shutil.rmtree(tmp_dir, ignore_errors=True)
