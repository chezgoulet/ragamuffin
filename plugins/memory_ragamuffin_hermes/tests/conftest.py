"""pytest configuration: mock Hermes agent modules before test imports."""
import sys
import types

# Create mock for agent.memory_provider before ANY plugin code is imported.
# The plugin __init__.py imports from agent.memory_provider, and this mock
# avoids requiring the full Hermes agent package at test time.

# 1) Create the parent "agent" module
_agent_mod = types.ModuleType("agent")
_agent_mod.__path__ = []  # mark as a namespace package

# 2) Create the "agent.memory_provider" module
_memory_provider_mod = types.ModuleType("agent.memory_provider")


class MockMemoryProvider:
    """Stand-in for the Hermes MemoryProvider ABC.

    Used in tests to verify method resolution without importing
    the real ABC (which requires the full Hermes agent package).
    """

    @property
    def name(self):
        raise NotImplementedError

    def is_available(self):
        raise NotImplementedError

    def initialize(self, session_id, **kwargs):
        raise NotImplementedError

    def get_tool_schemas(self):
        raise NotImplementedError


_memory_provider_mod.MemoryProvider = MockMemoryProvider

# 3) Inject into sys.modules BEFORE the plugin imports
sys.modules["agent"] = _agent_mod
sys.modules["agent.memory_provider"] = _memory_provider_mod
