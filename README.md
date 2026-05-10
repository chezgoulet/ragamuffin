# 🧣 ragamuffin

> *noun.* A person, typically a child, in ragged, dirty clothes.
> In our case: a scrappy little knowledge tool that agents can actually use.

---

**Ragamuffin** is what happens when you get tired of your RAG stack being held together with Python async bugs, MCP bridges, and two-hop architectures that fail at 1 AM.

Point it at a directory. It watches for changes, indexes everything into Qdrant, and serves a REST API that any agent can curl. No bridge. No translation layer. One binary.

```bash
curl -s -X POST http://localhost:8000/recall \
  -d '{"query":"what do we know about that thing?"}'
```

## What it does

- **🔍 /recall** — Semantic search. "What do we know about X?" → ranked chunks with sources.
- **🧠 /ask** — Synthesized answers via LLM. Cross-references multiple files, returns prose with citations.
- **✍️ /draft** — Write back. Agents contribute to the vault, not just read it. Direct mode or PR mode.
- **🩺 /audit** — Vault health check. Stale files, semantic conflicts, gaps, duplicates. The vault tests itself.

## Why

Every team running agents over a knowledge base hits the same wall: wiki → vector DB → agent access, with write-back. The existing tools are either enterprise SaaS with per-seat pricing, Python Jupyter notebooks in a trench coat, or abandoned side projects from 2024.

Ragamuffin is one static binary. You need Qdrant and an embedding API. That's it. LLM is optional. Git integration is optional. Everything else is optional. The only mandatory thing is that it works when you curl it.

## Quick Start

```bash
# You need Qdrant running somewhere
docker run -d -p 6333:6333 qdrant/qdrant

# Point Ragamuffin at your vault and go
docker run -d \
  -p 8000:8000 \
  -v /path/to/your/vault:/opt/vault:ro \
  -e RAGAMUFFIN_VAULT_PATH=/opt/vault \
  -e RAGAMUFFIN_QDRANT_URL=http://host.docker.internal:6333 \
  -e RAGAMUFFIN_EMBEDDING_API_KEY=sk-... \
  ghcr.io/chezgoulet/ragamuffin:latest

# Wait for indexing, then search
curl -s -X POST http://localhost:8000/recall \
  -d '{"query":"what should I know about this project?"}'
```

## Design

- **Go.** Single static binary. No runtime, no pip, no `asyncio.create_task` at module level.
- **REST-first.** MCP is a bolt-on. The curl test is the test that matters.
- **Optional everything.** Only Qdrant and an embedding API are mandatory. LLM? Optional. Git? Optional. Auth? Trust the proxy.
- **Write-back built in.** Agents learn things. The vault should grow.

## Status

Pre-release. [SPEC.md](SPEC.md) has the full architecture, endpoint reference, and environment variable table.

Named with affection by [Christopher Goulet](https://github.com/chezgoulet) and built with the hard-won wisdom of every Python async bug that crashed the MCP bridge at 1 AM.

---

*"The curl test is the test that matters."*
