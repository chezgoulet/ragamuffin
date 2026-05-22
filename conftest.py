"""Root conftest: mock Hermes agent modules before any package import.

This exists for running `pytest` from the project root targeting the
Hermes plugin tests. The plugin's own `tests/conftest.py` also seeds
these mocks as a fallback for running directly from the plugin directory.
"""
import sys
import types

# This runs BEFORE any package imports are resolved. Python auto-imports
# parent packages when loading conftest.py from subdirectories, which
# would trigger plugins.memory_ragamuffin_hermes.__init__ → from agent...
# We block that by pre-seeding sys.modules here at the project root.

_agent_mod = types.ModuleType("agent")
_agent_mod.__path__ = []

_memory_provider_mod = types.ModuleType("agent.memory_provider")


class _MockMemoryProvider:
    """Stand-in for Hermes MemoryProvider ABC — enough to satisfy isinstance checks."""
    @property
    def name(self):
        raise NotImplementedError
    def is_available(self):
        raise NotImplementedError
    def initialize(self, session_id, **kwargs):
        raise NotImplementedError
    def get_tool_schemas(self):
        raise NotImplementedError


_memory_provider_mod.MemoryProvider = _MockMemoryProvider
sys.modules["agent"] = _agent_mod
sys.modules["agent.memory_provider"] = _memory_provider_mod
