#!/bin/bash
# Automated backup script for Qdrant + SQLite (#815)
# Usage:
#   BACKUP_DIR=/mnt/backups RCLONE_DEST=s3:bucket/ragamuffin ./backup.sh
#   BACKUP_DIR=/mnt/backups ./backup.sh  (local-only)
#
# Backs up:
#   1. Qdrant snapshots (POST /collections/{name}/snapshots)
#   2. SQLite logstore DB (copy)
#   3. Vault directory (tar)
#   4. Retains last 7 snapshots, prunes older
#   5. Optional: pushes to remote via rclone

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/tmp/ragamuffin-backups}"
QDRANT_URL="${QDRANT_URL:-http://localhost:6334}"
RAGAMUFFIN_DATA="${RAGAMUFFIN_DATA:-/opt/vault}"
RETENTION="${RETENTION:-7}"
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)

mkdir -p "$BACKUP_DIR/$TIMESTAMP"

# ── 1. Qdrant snapshots ─────────────────────────────────────────────────────
echo "Backing up Qdrant collections..."
curl -sf "$QDRANT_URL/collections" | python3 -c "
import sys, json, subprocess
base = '$QDRANT_URL'
out = '$BACKUP_DIR/$TIMESTAMP'
try:
    data = json.load(sys.stdin)
    for name in data.get('result', {}).get('collections', []):
        name = name.get('name', '')
        if not name:
            continue
        print(f'  Snapshotting {name}...')
        subprocess.run(
            ['curl', '-sf', '-X', 'POST', f'{base}/collections/{name}/snapshots'],
            capture_output=True
        )
        print(f'  Done: {name}')
except Exception as e:
    print(f'  Error: {e}', file=sys.stderr)
" 2>&1 || echo "  Qdrant snapshot skipped (may not be available)"

# ── 2. SQLite logstore ──────────────────────────────────────────────────────
echo "Backing up SQLite logstore..."
LS_PATH="${RAGAMUFFIN_DATA}/.ragamuffin/logs.db"
if [ -f "$LS_PATH" ]; then
  cp "$LS_PATH" "$BACKUP_DIR/$TIMESTAMP/logs.db"
  echo "  Copied $LS_PATH"
else
  echo "  No logstore DB found at $LS_PATH"
fi

# ── 3. Vault directory ──────────────────────────────────────────────────────
echo "Backing up vault directory..."
VAULT_NAME=$(basename "$RAGAMUFFIN_DATA")
tar -czf "$BACKUP_DIR/$TIMESTAMP/vault.tar.gz" -C "$(dirname "$RAGAMUFFIN_DATA")" "$VAULT_NAME" 2>/dev/null || \
  tar -czf "$BACKUP_DIR/$TIMESTAMP/vault.tar.gz" -C /opt vault 2>/dev/null || \
  echo "  Vault backup skipped (directory not accessible)"

echo "Backup complete: $BACKUP_DIR/$TIMESTAMP"

# ── 4. Retention ────────────────────────────────────────────────────────────
echo "Pruning backups older than $RETENTION days..."
find "$BACKUP_DIR" -maxdepth 1 -type d -mtime "+$RETENTION" -exec rm -rf {} \; 2>/dev/null || true

# ── 5. Remote push (optional) ───────────────────────────────────────────────
if [ -n "${RCLONE_DEST:-}" ]; then
  if command -v rclone &>/dev/null; then
    echo "Pushing to rclone destination: $RCLONE_DEST"
    rclone copy "$BACKUP_DIR" "$RCLONE_DEST" --progress 2>&1 || true
  else
    echo "rclone not installed — skipping remote push"
  fi
fi

echo "Done."
