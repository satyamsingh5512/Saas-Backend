#!/usr/bin/env bash
# Phase 7 — Upload volume maintenance: orphan cleanup + quota report.
#
# Usage:
#   ./scripts/cleanup_uploads.sh [--delete] [--report]
#
# Background: file BYTES live on the uploads volume (/data/uploads, keyed by
# opaque storage_key); file OWNERSHIP lives in the `files` DB table. The two
# can drift when a delete removes the row but the unlink fails (crash between
# the two steps) or when an upload aborts after bytes land on disk.
#
#   --report (default): list volume files with no DB row + DB rows with no
#     volume file, and per-tenant byte totals vs plan quota. Read-only.
#   --delete: additionally remove orphaned volume files older than 24h
#     (grace period covers in-flight multipart uploads). DB rows pointing at
#     missing files are NEVER deleted here -- they need a product decision
#     (re-upload prompt vs tombstone), so they are only reported.
#
# Run weekly from cron. Orphans also surface in /metrics (saas_storage_bytes
# vs saas_storage_disk_used_bytes divergence) between runs.
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.production.yml}"
ENV_FILE="${ENV_FILE:-.env.production}"
MODE="report"
for arg in "$@"; do
  case "$arg" in
    --delete) MODE="delete" ;;
    --report) MODE="report" ;;
    *) echo "unknown arg: $arg (want --report|--delete)" >&2; exit 1 ;;
  esac
done

VOLUME="${COMPOSE_PROJECT_NAME:-saas}_uploads"
MNT="$(docker volume inspect -f '{{.Mountpoint}}' "$VOLUME" 2>/dev/null || true)"
if [[ -z "$MNT" ]]; then
  echo "uploads volume $VOLUME not found; is the stack up?" >&2
  exit 1
fi

echo "==> Volume files not referenced by any files.storage_key (orphans)"
# storage_key layout is tenant-scoped prefixes; compare basenames+paths.
TMPDB="$(mktemp)"; TMPVOL="$(mktemp)"; trap 'rm -f "$TMPDB" "$TMPVOL"' EXIT
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres psql -U postgres -d "${DB_NAME:-tenant_saas}" -Atc "SELECT storage_key FROM files;" | sort >"$TMPDB"
# Skip temp upload staging files (".upload-*") -- those are in-flight writes.
(cd "$MNT" && find . -type f ! -name ".upload-*" | sed 's|^\./||' | sort) >"$TMPVOL"
ORPHANS="$(comm -13 "$TMPDB" "$TMPVOL" || true)"
if [[ -z "$ORPHANS" ]]; then
  echo "none."
else
  echo "$ORPHANS" | while read -r f; do
    echo "  orphan: $f ($(du -h "$MNT/$f" | cut -f1))"
  done
fi

echo "==> DB rows whose bytes are missing from the volume"
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres psql -U postgres -d "${DB_NAME:-tenant_saas}" -Atc "SELECT id, tenant_id, storage_key FROM files;" |
  while IFS='|' read -r id tenant key; do
    if [[ ! -e "$MNT/$key" ]]; then
      echo "  missing bytes: file=$id tenant=$tenant key=$key (row kept; needs product triage)"
    fi
  done

echo "==> Per-tenant usage vs plan quota"
docker compose -f "$PROJECT_DIR/$COMPOSE_FILE" --env-file "$PROJECT_DIR/$ENV_FILE" \
  exec -T postgres psql -U postgres -d "${DB_NAME:-tenant_saas}" -c \
  "SELECT t.slug AS tenant, t.plan_code AS plan, COUNT(f.id) AS files, pg_size_pretty(COALESCE(SUM(f.size_bytes),0)) AS used
     FROM tenants t LEFT JOIN files f ON f.tenant_id = t.id
    GROUP BY t.slug, t.plan_code ORDER BY SUM(f.size_bytes) DESC NULLS LAST;"

if [[ "$MODE" == "delete" && -n "$ORPHANS" ]]; then
  echo "==> Deleting orphans older than 24h (grace period for in-flight uploads)"
  echo "$ORPHANS" | while read -r f; do
    if [[ -n "$f" ]] && find "$MNT/$f" -mmin +1440 -print -quit 2>/dev/null | grep -q .; then
      rm -f "$MNT/$f" && echo "  deleted: $f"
    elif [[ -n "$f" ]]; then
      echo "  kept (younger than 24h, may be in-flight): $f"
    fi
  done
  # Prune newly-empty tenant prefix directories (never the volume root).
  find "$MNT" -mindepth 1 -type d -empty -delete
fi
echo "done (mode=$MODE)."
