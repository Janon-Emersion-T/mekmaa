#!/usr/bin/env bash
set -euo pipefail

DB_PATH="${DB_PATH:-./app.db}"
UPLOAD_DIR="${UPLOAD_DIR:-./data/uploads}"
BACKUP_DEST="${BACKUP_DEST:-./backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
TARGET_DIR="${BACKUP_DEST%/}/mekmaa-${STAMP}"

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "sqlite3 is required for backups" >&2
  exit 1
fi

mkdir -p "$TARGET_DIR"

if [ ! -f "$DB_PATH" ]; then
  echo "database file not found: $DB_PATH" >&2
  exit 1
fi

if [ ! -d "$UPLOAD_DIR" ]; then
  echo "upload directory not found: $UPLOAD_DIR" >&2
  exit 1
fi

sqlite3 "$DB_PATH" ".backup '$TARGET_DIR/mekmaa.db'"
tar -C "$UPLOAD_DIR" -czf "$TARGET_DIR/uploads.tar.gz" .
sqlite3 "$TARGET_DIR/mekmaa.db" "PRAGMA integrity_check;" > "$TARGET_DIR/integrity-check.txt"

echo "Backup created at $TARGET_DIR"
