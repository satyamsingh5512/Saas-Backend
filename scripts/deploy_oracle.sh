#!/usr/bin/env bash
# Single-command production deploy to an Oracle Cloud Always Free VM.
#
# Usage (on the VM, from the repo root):
#   cp deploy/.env.production.example .env.production   # fill secrets once
#   DOMAIN=api.example.com ./scripts/deploy_oracle.sh
#
# Pipeline:
#   1. Preconditions (docker, env file, secrets, DNS hint).
#   2. Render deploy/nginx/nginx.conf -> deploy/nginx/rendered.conf.
#   3. Start postgres + redis, wait for health.
#   4. TLS: bootstrap config -> certbot webroot issuance (first run only) ->
#      full HTTPS config -> reload.
#   5. Build/start app + nginx, wait for app health (migrations run inside
#      the app at startup, before it starts serving).
#   6. End-to-end validation through the public hostname.
#
# Re-runs are safe (idempotent): existing volumes, certificates, and secrets
# are reused; `up -d` only recreates what changed.
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"
COMPOSE_FILE="docker-compose.production.yml"
ENV_FILE="${ENV_FILE:-.env.production}"
SKIP_TLS="${SKIP_TLS:-0}"   # SKIP_TLS=1 for DNS-not-ready smoke tests (HTTP only)

# shellcheck disable=SC1090
if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing $ENV_FILE. Create it first:" >&2
  echo "  cp deploy/.env.production.example $ENV_FILE  # then fill every CHANGE-ME" >&2
  exit 1
fi
set -a; source "$ENV_FILE"; set +a
DOMAIN="${DOMAIN:?Set DOMAIN in $ENV_FILE (e.g. DOMAIN=api.example.com)}"
APP_PULL_POLICY="${APP_PULL_POLICY:-build}"
APP_IMAGE="${APP_IMAGE:-tenant-saas-app:local}"

echo "==> [1/6] Preconditions"
command -v docker >/dev/null || { echo "docker not found; see docs/DEPLOY_ORACLE.md section 1." >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker daemon unreachable (user in docker group? try: sudo usermod -aG docker \$USER)." >&2; exit 1; }
for secret in DB_PASSWORD POSTGRES_PASSWORD REDIS_PASSWORD JWT_SECRET; do
  val="${!secret:-}"
  if [[ -z "$val" || "$val" == CHANGE-ME* ]]; then
    echo "Secret $secret is unset or still a placeholder in $ENV_FILE." >&2
    exit 1
  fi
done
if [[ "$DB_PASSWORD" == "$POSTGRES_PASSWORD" ]]; then
  echo "DB_PASSWORD and POSTGRES_PASSWORD must differ (least-privilege split)." >&2
  exit 1
fi
if [[ ${#JWT_SECRET} -lt 32 ]]; then
  echo "JWT_SECRET must be at least 32 characters." >&2
  exit 1
fi
echo "DNS check: $DOMAIN -> $(getent hosts "$DOMAIN" | awk '{print $1}' | head -1 || echo UNRESOLVED)"
echo "If that IP is not this VM, TLS issuance will fail -- fix the A record first (see docs)."

echo "==> [2/6] Rendering nginx config for $DOMAIN"
sed -e "s/__DOMAIN__/${DOMAIN}/g" -e "s/__APP_HOST__/app/g" \
  deploy/nginx/nginx.conf >deploy/nginx/rendered.conf
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" config >/dev/null
echo "compose file renders cleanly."

echo "==> [3/6] Starting postgres + redis"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d postgres redis
echo "waiting for data-plane health..."
for i in $(seq 1 30); do
  if docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" ps --format json 2>/dev/null | grep -q '"Health":"healthy"'; then
    break
  fi
  sleep 5
  if [[ $i -eq 30 ]]; then
    echo "postgres/redis did not become healthy; tailing logs:" >&2
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" logs --tail=50 postgres redis >&2
    exit 1
  fi
done

echo "==> [4/6] TLS"
CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
if [[ "$SKIP_TLS" == "1" ]]; then
  echo "SKIP_TLS=1: serving bootstrap HTTP only (smoke test mode, no cert)."
  cp deploy/nginx/bootstrap.conf deploy/nginx/rendered.conf
elif [[ -d "$CERT_DIR" ]]; then
  echo "certificate already present at $CERT_DIR; reusing (renewal is handled by the certbot timer)."
else
  echo "no certificate yet -- bootstrapping via ACME http-01."
  cp deploy/nginx/bootstrap.conf deploy/nginx/rendered.conf
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d nginx
  sleep 5
  if ! sudo -E DOMAIN="$DOMAIN" COMPOSE_FILE="$COMPOSE_FILE" ./scripts/ssl_setup.sh; then
    echo "TLS issuance failed. The stack is up on HTTP for debugging; fix DNS/firewall and re-run." >&2
    exit 1
  fi
  sed -e "s/__DOMAIN__/${DOMAIN}/g" -e "s/__APP_HOST__/app/g" \
    deploy/nginx/nginx.conf >deploy/nginx/rendered.conf
fi

echo "==> [5/6] Starting app + nginx"
if [[ "$APP_PULL_POLICY" == "missing" ]]; then
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" pull app
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-build
else
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --build
fi
echo "waiting for app health (includes startup migrations)..."
APP_OK=0
for i in $(seq 1 36); do
  if docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" exec -T app /tenant-saas -healthcheck >/dev/null 2>&1; then
    APP_OK=1
    break
  fi
  sleep 5
done
if [[ $APP_OK -ne 1 ]]; then
  echo "app did not become healthy; tailing logs:" >&2
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" logs --tail=80 app >&2
  exit 1
fi
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d nginx
sleep 3
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" ps

echo "==> [6/6] End-to-end validation"
check() {
  local name="$1" url="$2" expect="$3"
  local code
  code="$(curl -sk -o /dev/null -w "%{http_code}" --max-time 15 "$url" || echo 000)"
  if [[ "$code" == "$expect" ]]; then
    echo "  PASS $name ($url -> $code)"
  else
    echo "  FAIL $name ($url -> $code, want $expect)" >&2
    return 1
  fi
}
FAILED=0
if [[ "$SKIP_TLS" == "1" ]]; then
  check "landing (http bootstrap)" "http://127.0.0.1/" "503" || FAILED=1
else
  check "https landing" "https://$DOMAIN/" "200" || FAILED=1
  check "https health" "https://$DOMAIN/health" "200" || FAILED=1
  check "https ready" "https://$DOMAIN/health/ready" "200" || FAILED=1
  check "metrics (prometheus)" "https://$DOMAIN/metrics" "200" || FAILED=1
  check "swagger" "https://$DOMAIN/swagger" "200" || FAILED=1
  check "http->https redirect" "http://$DOMAIN/health" "301" || FAILED=1
  check "api 404 envelope" "https://$DOMAIN/api/v1/nope" "404" || FAILED=1
fi
if [[ $FAILED -ne 0 ]]; then
  echo "Validation failed -- inspect: docker compose -f $COMPOSE_FILE logs nginx app" >&2
  exit 1
fi

cat <<EOF

Deploy complete.
  App:      https://$DOMAIN/app
  Health:   https://$DOMAIN/health/ready
  Metrics:  https://$DOMAIN/metrics
  Swagger:  https://$DOMAIN/swagger
  Admin:    GET https://$DOMAIN/api/v1/admin/overview (org:view)

Remaining one-time setup (see docs/DEPLOY_ORACLE.md):
  - Nightly backups:   (crontab) 17 3 * * * $PROJECT_DIR/scripts/backup_postgres.sh
  - Upload janitor:    (weekly)  $PROJECT_DIR/scripts/cleanup_uploads.sh --delete
  - TLS renewal:       installed by scripts/ssl_setup.sh (systemd timer or cron)
EOF
