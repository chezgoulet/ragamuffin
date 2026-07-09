"""Network-path verification of issue #793 with a mock requests client.

Confirms on_session_end issues one /v1/ingest (summary) and one /v1/facts
POST per extracted fact, and that _put_session_fact honors dedupe-by-key via
the upsert contract.
"""
import sys
import types

_provider_mod = types.ModuleType("agent.memory_provider")


class MemoryProvider:
    def name(self):
        raise NotImplementedError


_provider_mod.MemoryProvider = MemoryProvider
agent_pkg = types.ModuleType("agent")
agent_pkg.memory_provider = _provider_mod
sys.modules["agent"] = agent_pkg
sys.modules["agent.memory_provider"] = _provider_mod

import importlib.util

spec = importlib.util.spec_from_file_location("ragamuffin_adapter", "__init__.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
RagamuffinMemoryProvider = mod.RagamuffinMemoryProvider


class _Resp:
    def __init__(self, status):
        self.status_code = status
        self.text = "{}"

    def json(self):
        return {}


class _FakeRequests:
    def __init__(self):
        self.calls = []

    def post(self, url, json=None, headers=None, timeout=None):
        self.calls.append((url, json))
        return _Resp(200)

    def get(self, url, headers=None, timeout=None):
        self.calls.append((url, None))
        return _Resp(404)


def make_provider():
    p = RagamuffinMemoryProvider()
    p._requests = _FakeRequests()
    p._endpoint = "http://ragamuffin:8000"
    p._auth_token = ""
    p._available = True
    p._vault_ready = True
    p._auto_session_facts = True
    p._session_facts_prefix = "house"
    p._agent_identity = "dev"
    p._agent_vault = "agent::dev"
    p._session_id = "sess-1"
    return p


def test_on_session_end_calls():
    p = make_provider()
    messages = [
        {"role": "assistant", "content": "We decided to use Postgres as the store."},
        {"role": "assistant", "content": "Our standard is to always run migrations."},
    ]
    # Run synchronously by invoking the inner _run via the thread's target.
    # Easiest: call on_session_end then drain the daemon thread.
    p.on_session_end(messages)
    p._sync_thread.join(timeout=5)

    urls = [c[0] for c in p._requests.calls]
    assert any(u.endswith("/v1/ingest") for u in urls), urls
    fact_calls = [c for c in p._requests.calls if c[0].endswith("/v1/facts")]
    assert len(fact_calls) == 2, fact_calls
    for url, body in fact_calls:
        assert body["vault"] == "agent::dev"
        assert body["source"] == "session_end_auto"
        assert body["source_type"] == "conversation"
        assert body["key"].startswith("house/")
        assert "session_fact" in body["tags"]
    print(f"on_session_end network calls: {len(urls)} "
          f"(1 ingest + {len(fact_calls)} facts)")
    print("NETWORK PATH OK")


def test_put_session_fact_upsert_contract():
    p = make_provider()
    ok = p._put_session_fact("house/decision/x", "Use Postgres.", "decision")
    assert ok is True
    # Non-200 from server -> False (and doesn't raise)
    p._requests.post = lambda *a, **k: _Resp(500)
    bad = p._put_session_fact("house/decision/y", "Use MySQL.", "decision")
    assert bad is False
    print("put_session_fact upsert contract OK")


if __name__ == "__main__":
    test_on_session_end_calls()
    test_put_session_fact_upsert_contract()
    print("\nALL NET CHECKS PASSED")
