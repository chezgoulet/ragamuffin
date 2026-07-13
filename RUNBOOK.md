# RUNBOOK — Ragamuffin Incident Recovery

## 1. Qdrant Corruption or Data Loss

**Symptoms:**
- `/health` shows `qdrant: down` or `qdrant: reconnecting`
- `/recall` returns 0 results or `QDRANT_UNREACHABLE`

**Recovery:**
```bash
# 1. Check Qdrant container status
docker logs qdrant --tail 50

# 2. If volume is corrupted, restore from last Qdrant snapshot
# Snapshots are at /qdrant/snapshots/ inside the container
docker exec qdrant ls /qdrant/snapshots/

# 3. Restore a snapshot:
curl -X POST "http://qdrant:6333/collections/ragamuffin/snapshots/recover" \
  -H "Content-Type: application/json" \
  -d '{"location": "file:///qdrant/snapshots/<snapshot_name>"}'

# 4. If no snapshot exists, re-index from vault files:
curl -X POST http://ragamuffin:8000/reindex
```

**Prevention:** Run `scripts/backup.sh` regularly (see #815).

---

## 2. SQLite Logstore Loss

**Symptoms:**
- Fact operations return 500
- `/v1/logs` returns 0 results or 500 error
- Link queries return empty

**Recovery:**
```bash
# 1. Check SQLite file exists
ls -la /opt/vault/.ragamuffin/logs.db

# 2. If missing, restore from backup
cp /backups/logs-$(date +%F).db /opt/vault/.ragamuffin/logs.db

# 3. If no backup exists, restart Ragamuffin — it creates a fresh DB
# Facts in Qdrant are still intact; only history and links are lost
```

**Data loss:** Qdrant fact data survives. Only session history, link index, and review resolutions are lost. These can be rebuilt from fact metadata.

---

## 3. Embedding API Outage

**Symptoms:**
- `/health` shows `embedding: down`
- `/recall` and `/ask` return `EMBEDDING_API_ERROR`

**Recovery:**
```bash
# 1. Check embedding service status
curl -sf http://embedding-api:8000/health

# 2. Restart the embedding service
docker compose restart embedding

# 3. If persistent, switch provider:
# Set RAGAMUFFIN_EMBEDDING_BASE_URL to a fallback endpoint
# Restart Ragamuffin to pick up the change
```

**During outage:** Existing Qdrant embeddings are still stored. New indexing and search is blocked until the embedding API recovers.

---

## 4. OOM Kill

**Symptoms:**
- Container exits with code 137
- Kernel log shows `oom-killer`

**Recovery:**
```bash
# 1. Check memory limits
docker inspect ragamuffin | jq '.[0].HostConfig.Memory'

# 2. Increase memory limit
# Docker: --memory=512m
# Compose: deploy.resources.limits.memory: 512M

# 3. The export handler is the most common OOM trigger
# Use /vault/{name}/v1/snapshot instead of export for large vaults
```

---

## 5. Configuration Corruption

**Symptoms:**
- Server fails to start
- Logs show config validation errors

**Recovery:**
```bash
# 1. Check environment variables
docker inspect ragamuffin | jq '.[0].Config.Env'

# 2. Required vars: RAGAMUFFIN_QDRANT_URL, RAGAMUFFIN_EMBEDDING_API_KEY
# Required (one of): RAGAMUFFIN_VAULT_PATH or RAGAMUFFIN_VAULTS

# 3. Validate config via health endpoint
curl http://ragamuffin:8000/version
```
