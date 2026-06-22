"""Shared fixtures and helpers for Hermes memory adapter tests.

Note: the adapter lives at adapters/hermes-memory/__init__.py (directory name
contains a hyphen), so it is not importable as a regular Python module.
We load it via importlib and inject it into sys.modules under a synthetic
name for testing convenience.
"""

import importlib.util
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

# ---------------------------------------------------------------------------
# Load the adapter module under a friendly name
# ---------------------------------------------------------------------------

_ADAPTER_PATH = Path(__file__).resolve().parent.parent / "__init__.py"

_hermes_memory_mod = None


def _get_adapter_module():
    global _hermes_memory_mod
    if _hermes_memory_mod is not None:
        return _hermes_memory_mod

    if str(_ADAPTER_PATH) in sys.modules:
        _hermes_memory_mod = sys.modules[str(_ADAPTER_PATH)]
        return _hermes_memory_mod

    spec = importlib.util.spec_from_file_location(
        "adapters.hermes_memory", str(_ADAPTER_PATH)
    )
    mod = importlib.util.module_from_spec(spec)
    # The module imports from agent.memory_provider which won't be available
    # in the test env. We mock it before loading.
    class FakeMemoryProvider:
        """Minimal stand-in for Hermes' MemoryProvider base class."""
        pass

    mock_agent = MagicMock()
    mock_agent.memory_provider = MagicMock()
    mock_agent.memory_provider.MemoryProvider = FakeMemoryProvider
    sys.modules["agent"] = mock_agent
    sys.modules["agent.memory_provider"] = mock_agent.memory_provider
    sys.modules["agent"] = MagicMock()
    sys.modules["agent.memory_provider"] = mock_memory_provider

    sys.modules["adapters.hermes_memory"] = mod
    spec.loader.exec_module(mod)
    _hermes_memory_mod = mod
    return mod


@pytest.fixture(scope="session")
def hermes_memory_mod():
    return _get_adapter_module()


# ---------------------------------------------------------------------------
# A mock HTTP response for requests.post / requests.get
# ---------------------------------------------------------------------------


class MockResponse:
    """Drop-in mock for ``requests.Response``."""

    def __init__(
        self,
        status_code: int = 200,
        json_data: Any = None,
        text: str = "",
    ):
        self.status_code = status_code
        self.ok = 200 <= status_code < 300
        self._json_data = json_data
        self._text = text

    def json(self) -> Any:
        return self._json_data if self._json_data is not None else {}

    @property
    def text(self) -> str:
        return self._text if self._text else json.dumps(self._json_data or {})


# ---------------------------------------------------------------------------
# Monkey-patch requests module so tests don't need the real library
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def _fake_requests():
    """Replace the real ``requests`` module with a MagicMock.

    Individual tests or fixtures can configure ``requests.post.return_value``
    or ``requests.get.return_value`` before exercising the provider.
    """
    fake = MagicMock()
    fake.post.return_value = MockResponse(200, {"results": []})
    fake.get.return_value = MockResponse(200, {"vaults": []})
    with patch.dict("sys.modules", {"requests": fake}):
        yield fake


@pytest.fixture
def fake_requests(_fake_requests):
    """Convenience alias — the mocked requests module for this test."""
    return _fake_requests


# ---------------------------------------------------------------------------
# Fresh provider instance (minimal config)
# ---------------------------------------------------------------------------


@pytest.fixture
def provider(hermes_memory_mod, _fake_requests):
    """Return a ``RagamuffinMemoryProvider`` with env-var-based config."""
    env = {
        "RAGAMUFFIN_ENDPOINT": "http://test-ragamuffin:8000",
        "RAGAMUFFIN_AUTH_TOKEN": "test-token",
        "RAGAMUFFIN_VAULT_PREFIX": "test::",
    }
    with patch.dict(os.environ, env, clear=True):
        prov = hermes_memory_mod.RagamuffinMemoryProvider()
        yield prov


# ---------------------------------------------------------------------------
# Temp config file fixture
# ---------------------------------------------------------------------------


@pytest.fixture
def temp_config_file():
    """Create a temporary ``ragamuffin.json`` and yield its path."""
    data = {
        "endpoint": "http://file-config:8000",
        "vault_prefix": "file::",
    }
    tmp = tempfile.NamedTemporaryFile(
        mode="w", suffix=".json", delete=False
    )
    json.dump(data, tmp)
    tmp.close()
    yield Path(tmp.name)
    if Path(tmp.name).exists():
        os.unlink(tmp.name)
