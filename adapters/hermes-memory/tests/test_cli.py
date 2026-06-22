"""Tests for CLI subcommands — setup, status, doctor.

These tests exercise the ``cmd_*`` functions directly and validate
config file behavior, rather than invoking the argument parser.
"""

import json
import os
from pathlib import Path
from unittest.mock import patch

import pytest


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def cli_mod(hermes_memory_mod):
    """Load the CLI module via importlib from the same parent directory."""
    import importlib.util
    import sys

    cli_path = Path(hermes_memory_mod.__file__).parent / "cli.py"
    spec = importlib.util.spec_from_file_location("adapters.hermes_memory.cli", str(cli_path))
    mod = importlib.util.module_from_spec(spec)
    # Need hermes_memory_mod to be importable for the `.cli` import
    sys.modules["adapters"] = type(sys)("adapters")
    sys.modules["adapters.hermes_memory"] = hermes_memory_mod
    sys.modules["adapters.hermes_memory.cli"] = mod
    spec.loader.exec_module(mod)
    return mod


@pytest.fixture
def tmp_hermes_home(tmp_path):
    """Set HERMES_HOME to a temp dir and return it."""
    with patch.dict(os.environ, {"HERMES_HOME": str(tmp_path)}):
        yield tmp_path


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSetup:
    """``hermes ragamuffin setup`` writes config to ragamuffin.json."""

    def test_setup_writes_config(self, cli_mod, tmp_hermes_home):
        # Simulate argparse namespace
        args = type("Args", (), {
            "endpoint": "http://my-ragamuffin:8000",
            "auth_token": "my-token",
            "vault_prefix": "my::",
            "recall_mode": "hybrid",
            "save_messages": True,
            "context_cadence": 5,
            "dialectic_cadence": 10,
        })()

        cli_mod.cmd_setup(args)

        config_file = tmp_hermes_home / "ragamuffin.json"
        assert config_file.exists()
        data = json.loads(config_file.read_text())
        assert data["endpoint"] == "http://my-ragamuffin:8000"
        assert data["auth_token"] == "my-token"
        assert data["vault_prefix"] == "my::"

    def test_setup_overwrites_existing(self, cli_mod, tmp_hermes_home):
        # Write existing config
        (tmp_hermes_home / "ragamuffin.json").write_text(
            json.dumps({"endpoint": "http://old:8000", "vault_prefix": "old::"})
        )

        args = type("Args", (), {
            "endpoint": "http://new:8000",
            "auth_token": "",
            "vault_prefix": "new::",
            "recall_mode": "context",
            "save_messages": False,
            "context_cadence": 1,
            "dialectic_cadence": 2,
        })()
        cli_mod.cmd_setup(args)

        data = json.loads((tmp_hermes_home / "ragamuffin.json").read_text())
        assert data["endpoint"] == "http://new:8000"
        assert data["vault_prefix"] == "new::"

    def test_setup_no_auth_token(self, cli_mod, tmp_hermes_home):
        """Empty auth_token should not be written."""
        args = type("Args", (), {
            "endpoint": "http://r:8000",
            "auth_token": "",
            "vault_prefix": "agent::",
            "recall_mode": "hybrid",
            "save_messages": True,
            "context_cadence": 3,
            "dialectic_cadence": 5,
        })()
        cli_mod.cmd_setup(args)

        data = json.loads((tmp_hermes_home / "ragamuffin.json").read_text())
        assert "auth_token" not in data


class TestStatus:
    """``hermes ragamuffin status`` displays config info."""

    def test_status_no_config(self, cli_mod, tmp_hermes_home, capsys):
        cli_mod.cmd_status(None)
        captured = capsys.readouterr()
        assert "no ragamuffin.json" in captured.out.lower()

    def test_status_with_config(self, cli_mod, tmp_hermes_home, capsys):
        (tmp_hermes_home / "ragamuffin.json").write_text(
            json.dumps({"endpoint": "http://r:8000", "vault_prefix": "env::"})
        )
        cli_mod.cmd_status(None)
        captured = capsys.readouterr()
        assert "http://r:8000" in captured.out
        assert "env::" in captured.out


class TestDoctor:
    """``hermes ragamuffin doctor`` runs validation checks."""

    def test_doctor_no_config(self, cli_mod, tmp_hermes_home, capsys):
        cli_mod.cmd_doctor(None)
        captured = capsys.readouterr()
        assert "issue" in captured.out.lower() or "no ragamuffin.json" in captured.out.lower()

    def test_doctor_healthy_config(self, cli_mod, tmp_hermes_home, capsys):
        (tmp_hermes_home / "ragamuffin.json").write_text(
            json.dumps({"endpoint": "http://ragamuffin:8000", "vault_prefix": "agent::"})
        )
        cli_mod.cmd_doctor(None)
        captured = capsys.readouterr()
        assert "all checks passed" in captured.out.lower() or "info" in captured.out.lower()

    def test_doctor_no_endpoint(self, cli_mod, tmp_hermes_home, capsys):
        (tmp_hermes_home / "ragamuffin.json").write_text(
            json.dumps({"vault_prefix": "agent::"})
        )
        cli_mod.cmd_doctor(None)
        captured = capsys.readouterr()
        assert "no endpoint" in captured.out.lower() or "no ragamuffin endpoint" in captured.out.lower()

    def test_doctor_invalid_recall_mode(self, cli_mod, tmp_hermes_home, capsys):
        (tmp_hermes_home / "ragamuffin.json").write_text(
            json.dumps({"endpoint": "http://r:8000", "recall_mode": "invalid"})
        )
        cli_mod.cmd_doctor(None)
        captured = capsys.readouterr()
        assert "invalid recall_mode" in captured.out.lower()
