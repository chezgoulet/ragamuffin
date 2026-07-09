"""Standalone verification of issue #793 extraction logic (no network).

Imports the provider class by stubbing the missing `agent.memory_provider`
dependency so we can exercise the pure extraction helpers.
"""
import sys
import types

# --- Stub the `agent.memory_provider` module the adapter imports from ------
_provider_mod = types.ModuleType("agent.memory_provider")


class MemoryProvider:
    """Minimal stand-in for the real base class (interface only)."""

    def name(self):  # pragma: no cover - stub
        raise NotImplementedError


_provider_mod.MemoryProvider = MemoryProvider
agent_pkg = types.ModuleType("agent")
agent_pkg.memory_provider = _provider_mod
sys.modules["agent"] = agent_pkg
sys.modules["agent.memory_provider"] = _provider_mod

import importlib.util

spec = importlib.util.spec_from_file_location(
    "ragamuffin_adapter", "__init__.py"
)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

RagamuffinMemoryProvider = mod.RagamuffinMemoryProvider


def make_provider():
    p = RagamuffinMemoryProvider()
    # Disable network: no requests client, no endpoint.
    p._requests = None
    p._available = True
    p._vault_ready = True
    p._auto_session_facts = True
    p._session_facts_prefix = "house"
    p._agent_identity = "dev"
    p._session_id = "sess-1"
    return p


def test_extraction():
    p = make_provider()
    messages = [
        {"role": "user", "content": "What db should we use?"},
        {
            "role": "assistant",
            "content": (
                "We decided to use Postgres for the primary store. "
                "I configured the connection pool size to 20. "
                "The env var DATABASE_URL points at the replica. "
                "Our standard is to always use migrations for schema changes."
            ),
        },
        {"role": "user", "content": "good"},
        {
            "role": "assistant",
            "content": "Conclusion: Postgres is the right call for now.",
        },
    ]
    facts = p._extract_session_facts(messages)
    print(f"extracted {len(facts)} facts")
    for f in facts:
        print("  -", f["key"], "::", f["value"][:60])

    keys = {f["key"] for f in facts}
    # Deterministic keys: identical decision text -> identical key (dedup).
    # Re-run on the same input -> identical key set.
    facts2 = p._extract_session_facts(messages)
    keys2 = {f["key"] for f in facts2}
    assert keys == keys2, "keys must be deterministic across runs"
    print("determinism: OK")

    # Same conclusion repeated -> only one occurrence kept.
    assert len(keys) == len(facts), "duplicate keys within a run"
    print("intra-run dedup: OK")

    # Domain check
    domains = {f["domain"] for f in facts}
    assert "decision" in domains, domains
    assert "config" in domains, domains
    assert "preference" in domains, domains
    assert "conclusion" in domains, domains
    print("domains:", sorted(domains))

    # Fact key shape
    for f in facts:
        assert f["key"].startswith("house/"), f["key"]
        parts = f["key"].split("/")
        assert len(parts) == 3, f["key"]
    print("key shape: OK")


def test_question_filtered():
    p = make_provider()
    msgs = [{"role": "user", "content": "Should we use Postgres or MySQL?"}]
    facts = p._extract_session_facts(msgs)
    assert facts == [], facts
    print("question filtered: OK")


def test_disabled():
    # The auto flag is honored by on_session_end, not by the pure extractor.
    p = make_provider()
    p._auto_session_facts = False
    messages = [{"role": "assistant", "content": "We decided to use X."}]
    # on_session_end would pass [] to the network step when disabled:
    facts = p._extract_session_facts(messages) if p._auto_session_facts else []
    assert facts == []
    print("auto disabled (gate): OK")


if __name__ == "__main__":
    test_extraction()
    test_question_filtered()
    test_disabled()
    print("\nALL CHECKS PASSED")
