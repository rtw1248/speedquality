#!/usr/bin/env bash
set -euo pipefail

SQ_STATE_DIR="${SQ_STATE_DIR:-/opt/speedquality/state}"
SQ_BACKUP_ROOT="${SQ_BACKUP_ROOT:-/opt/speedquality/backups}"
SQ_BACKUP_IMAGE="${SQ_BACKUP_IMAGE:-speedquality-platform:1.2.0-redis}"
SQ_BACKUP_DEST="$SQ_BACKUP_ROOT/$(date -u +%Y%m%dT%H%M%SZ)"

# Host paths remain identical inside the short-lived management container.
umask 077
mkdir -p "$SQ_BACKUP_ROOT"
exec docker run --rm --user 0:0 \
  -v "$SQ_STATE_DIR:$SQ_STATE_DIR" \
  -v "$SQ_BACKUP_ROOT:$SQ_BACKUP_ROOT" \
  "$SQ_BACKUP_IMAGE" node /app/deploy/vps/admin.mjs backup \
  --public-dir "$SQ_STATE_DIR/data/public" \
  --core-dir "$SQ_STATE_DIR/data/core" \
  --output "$SQ_BACKUP_DEST"
