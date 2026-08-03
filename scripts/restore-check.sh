#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <backup-directory>" >&2
  exit 1
fi

BACKUP_DIR="$1"
DB_BACKUP="$BACKUP_DIR/mekmaa.db"
UPLOAD_BACKUP="$BACKUP_DIR/uploads.tar.gz"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "sqlite3 is required for restore verification" >&2
  exit 1
fi

if [ ! -f "$DB_BACKUP" ]; then
  echo "backup database not found: $DB_BACKUP" >&2
  exit 1
fi

if [ ! -f "$UPLOAD_BACKUP" ]; then
  echo "backup upload archive not found: $UPLOAD_BACKUP" >&2
  exit 1
fi

cp "$DB_BACKUP" "$WORK_DIR/verify.db"
sqlite3 "$WORK_DIR/verify.db" "PRAGMA integrity_check;" | tee "$WORK_DIR/integrity-check.txt"

mkdir -p "$WORK_DIR/uploads"
tar -C "$WORK_DIR/uploads" -xzf "$UPLOAD_BACKUP"

echo "Backup verification succeeded for $BACKUP_DIR"
echo "Restored files:"
find "$WORK_DIR/uploads" -maxdepth 2 -type f | sort
