# USER.md — Setup Guide

## Prerequisites

- OpenClaw running (any version)
- Ragamuffin accessible at `http://ragamuffin:8000` (or configure URL)
- Optional: `RAGAMUFFIN_API_KEY` if API key auth is enabled

## Setup Steps

1. **Copy the agent directory** to your OpenClaw workspace:
   ```bash
   cp -r reference/ragamuffin-agent /path/to/openclaw/workspace/agents/my-agent
   ```

2. **Customize the personality:**
   - Edit `AGENTS.md` — replace the template with your agent's context
   - Edit `SOUL.md` — adjust voice and values to match your needs
   - Edit `IDENTITY.md` — set your agent ID

3. **Configure OpenClaw:**
   See `openclaw-config.json.example` for the full config. At minimum:
   ```json
   {
     "agents": {
       "my-agent": {
         "personality": "agents/my-agent",
         "tools": ["exec", "web_search", "web_fetch", ...],
         "env": {
           "RAGAMUFFIN_URL": "http://ragamuffin:8000"
         }
       }
     }
   }
   ```

4. **Verify connectivity:**
   ```bash
   curl -s http://ragamuffin:8000/health
   ```

5. **Test ingest + recall:**
   ```bash
   curl -s -X POST http://ragamuffin:8000/v1/ingest \
     -H "Content-Type: application/json" \
     -d '{"content": "Hello world", "source": "test.md"}'

   curl -s -X POST http://ragamuffin:8000/recall \
     -H "Content-Type: application/json" \
     -d '{"query": "hello"}'
   ```

## Agent Operator

- **Name**: [Your name]
- **Role**: Operator
- **Communication**: Prefer [Telegram | Discord | Signal | etc.]
- **Availability**: [When the agent should act vs. wait]
