#!/usr/bin/env bash
# Phase 13 — Restore a PostgreSQL custom-format dump (companion to backup_postgres.sh).
#
# Usage:
#   ./scripts/restore_postgres.sh /var/backups/saas-postgres/pg_tenant_saas_20260101T030000Z.dump [target_db]
#
# Safety:
#   - Refuses to overwrite the live database without typing the database name.
#   - Verifies the sha256 sidecar when present.
#   - Restores with --no-owner/--role mapping so dumps taken as the postgres
#     superuser replay cleanly under the compose-managed roles.
set -euo pipefail

DUMP="${1:?Usage: restore_postgres.sh <dump-file> [target_db]}"
TARGET_DB="${2:-}"
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.production.yml}"
ENV_FILE="${ENV_FILE:-.env.production}"

if [[ ! -f "$DUMP" ]]; then
  echo "dump file not found: $DUMP" >&2
  exit 1
fi
if [[ -f "$DUMP.sha256" ]]; then
  echo "==> Verifying checksum"
  (cd "$(dirname "$DUMP")" && sha256sum -c "$(basename "$DUMP").sha256")
fi

# shellcheck disable=SC1090
set -a; source "$PROJECT_DIR/$ENV_FILE"; set +a
TARGET_DB="${TARGET_DB:-$DB_NAME}"

if [[ "$TARGET_DB" == "$DB_NAME" ]]; then
  echo "WARNING: this will REPLACE the live database '$TARGET_DB'." >&2
  read -rp "Type the database name to confirm: " CONFIRM
  if [[ "$CONFIRM" != "$TARGET_DB" ]]; then
    echo "Aborted." >&2
    exit 1
  fi
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TARGET_DB' AND pid <> pg_backend_pid();"
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "DROP DATABASE \"$TARGET_DB\";"
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "CREATE DATABASE \"$TARGET_DB\";"
else
  docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
    exec -T postgres psql -U postgres -d postgres -c "CREATE DATABASE \"$TARGET_DB\";" || true
fi

echo "==> Restoring $DUMP -> $TARGET_DB"
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres pg_restore -U postgres -d "$TARGET_DB" --no-owner --role=postgres <"$DUMP"

echo "==> Re-provisioning app_user grants on restored schema"
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres psql -U postgres -d "$TARGET_DB" \
  -c "GRANT USAGE ON SCHEMA public TO ${DB_USER:-app_user}; GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ${DB_USER:-app_user}; GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ${DB_USER:-app_user}; REVOKE UPDATE, DELETE ON audit_logs FROM ${DB_USER:-app_user};"

echo "Restore complete. Restart the app to pick up the restored data cleanly:"
echo "  docker compose -f $COMPOSE_FILE --env-file $ENV_FILE restart app"
