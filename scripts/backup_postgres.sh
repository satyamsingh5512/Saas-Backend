#!/usr/bin/env bash
# Phase 13 — Nightly PostgreSQL backup with 7-day retention.
#
# Usage (cron on the VM, or CI schedule):
#   DB_NAME=tenant_saas BACKUP_DIR=/var/backups/saas-postgres ./scripts/backup_postgres.sh
#
# What it does:
#   - pg_dump in custom format (-Fc: compressed, parallel-restorable, and
#     schema+data in one file) streamed from the postgres container -- no
#     credentials on the command line, no dump ever written inside the
#     container's writable layer.
#   - gzip level is left to pg_dump's built-in compression (-Fc compresses
#     by default); the file is checksummed (sha256) for restore verification.
#   - Retention: keeps the newest 7 daily dumps, deletes older ones.
#   - Optional off-VM copy: set S3_DEST=s3://bucket/prefix and an AWS CLI
#     with credentials; the upload runs only after a successful local write.
#   - Optional restore self-test: set RESTORE_TEST=1 to restore into a
#     scratch database and drop it, proving the dump is actually loadable
#     (a backup that has never been restored is a rumor).
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.production.yml}"
ENV_FILE="${ENV_FILE:-.env.production}"
DB_NAME="${DB_NAME:-tenant_saas}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/saas-postgres}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
S3_DEST="${S3_DEST:-}"
RESTORE_TEST="${RESTORE_TEST:-0}"

mkdir -p "$BACKUP_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DUMP="$BACKUP_DIR/pg_${DB_NAME}_${STAMP}.dump"

echo "==> Dumping database $DB_NAME -> $DUMP"
# shellcheck disable=SC2094
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres pg_dump -U postgres -Fc "$DB_NAME" >"$DUMP"
sha256sum "$DUMP" >"$DUMP.sha256"
echo "wrote $(du -h "$DUMP" | cut -f1) $(basename "$DUMP")"

echo "==> Verifying dump header"
if ! docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres pg_restore --list "$DUMP" >/dev/null 2>&1; then
  # pg_restore --list needs the file inside the container; fall back to a
  # header magic check on the host copy (custom format starts with "PGDMP").
  if ! head -c 5 "$DUMP" | grep -q "PGDMP"; then
    echo "ERROR: dump failed verification; keeping file for inspection, aborting rotation." >&2
    exit 1
  fi
fi

if [[ "$RESTORE_TEST" == "1" ]]; then
  echo "==> Restore self-test into scratch database"
  SCRATCH="restore_test_${STAMP//[^0-9]/}"
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "CREATE DATABASE \"$SCRATCH\";" >/dev/null
  # Stream the local dump back through stdin (no container file staging).
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres pg_restore -U postgres -d "$SCRATCH" --no-owner --role=postgres <"$DUMP" >/dev/null
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "DROP DATABASE \"$SCRATCH\";" >/dev/null
  echo "restore self-test passed."
fi

if [[ -n "$S3_DEST" ]]; then
  echo "==> Uploading off-VM copy to $S3_DEST"
  aws s3 cp "$DUMP" "$S3_DEST/$(basename "$DUMP")" --only-show-errors
  aws s3 cp "$DUMP.sha256" "$S3_DEST/$(basename "$DUMP").sha256" --only-show-errors
fi

echo "==> Applying ${RETENTION_DAYS}-day retention"
find "$BACKUP_DIR" -maxdepth 1 -name "pg_${DB_NAME}_*.dump" -mtime +"$RETENTION_DAYS" -delete
find "$BACKUP_DIR" -maxdepth 1 -name "pg_${DB_NAME}_*.dump.sha256" -mtime +"$RETENTION_DAYS" -delete
echo "backups retained: $(ls "$BACKUP_DIR"/pg_"$DB_NAME"_*.dump 2>/dev/null | wc -l)"
