#!/usr/bin/env bash
# Phase 4 — TLS issuance + auto-renewal (Let's Encrypt / Certbot).
#
# Usage:
#   sudo DOMAIN=api.example.com EMAIL=ops@example.com ./scripts/ssl_setup.sh
#
# What it does:
#   1. Installs certbot (snap preferred on Ubuntu 24.04, apt fallback).
#   2. Issues/renews a certificate with the webroot plugin against the
#      certbot-www Docker volume's host path (no nginx downtime, no
#      standalone port-80 grab). The nginx bootstrap config must already be
#      serving /.well-known/acme-challenge (scripts/deploy_oracle.sh does this).
#   3. Installs auto-renewal: a systemd timer when systemd is present,
#      otherwise a twice-daily cron entry -- both run `certbot renew` with a
#      deploy hook that reloads nginx only when a certificate actually changed.
#
# Renewal hook reloads the containerized nginx (not host nginx, which does
# not exist here) -- this is the step most Certbot guides get wrong for
# Docker deployments.
set -euo pipefail

DOMAIN="${DOMAIN:?Set DOMAIN, e.g. DOMAIN=api.example.com}"
EMAIL="${EMAIL:-}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.production.yml}"
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

if [[ $EUID -ne 0 ]]; then
  echo "Run as root (sudo) -- certbot writes /etc/letsencrypt." >&2
  exit 1
fi

echo "==> [1/4] Installing certbot"
if command -v snap >/dev/null 2>&1; then
  snap install core 2>/dev/null || true
  snap refresh core 2>/dev/null || true
  snap install --classic certbot 2>/dev/null || true
  ln -sf /snap/bin/certbot /usr/bin/certbot
elif ! command -v certbot >/dev/null 2>&1; then
  apt-get update -y
  DEBIAN_FRONTEND=noninteractive apt-get install -y certbot
fi
certbot --version

echo "==> [2/4] Resolving certbot webroot path"
# The certbot-www named volume's host mountpoint (created by compose up).
VOLUME="${COMPOSE_PROJECT_NAME:-saas}_certbot_www"
WEBROOT="$(docker volume inspect -f '{{.Mountpoint}}' "$VOLUME" 2>/dev/null || true)"
if [[ -z "$WEBROOT" ]]; then
  echo "certbot-www volume not found; is the stack up? Run ./scripts/deploy_oracle.sh first." >&2
  exit 1
fi
echo "webroot: $WEBROOT"

echo "==> [3/4] Issuing certificate for $DOMAIN"
EMAIL_ARGS=("--non-interactive" "--agree-tos" "--no-eff-email")
if [[ -n "$EMAIL" ]]; then
  EMAIL_ARGS=("--non-interactive" "--agree-tos" "--email" "$EMAIL")
fi
certbot certonly --webroot -w "$WEBROOT" -d "$DOMAIN" "${EMAIL_ARGS[@]}" \
  --deploy-hook "docker compose -f $PROJECT_DIR/$COMPOSE_FILE exec -T nginx nginx -s reload"

echo "==> [4/4] Installing auto-renewal"
# Prefer systemd (Ubuntu 24.04 default); cron otherwise. Both are idempotent.
if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
  cat >/etc/systemd/system/certbot-renew-saas.service <<EOF
[Unit]
Description=Renew SaaS TLS certificates
[Service]
Type=oneshot
ExecStart=/usr/bin/certbot renew --quiet --deploy-hook "docker compose -f $PROJECT_DIR/$COMPOSE_FILE exec -T nginx nginx -s reload"
EOF
  cat >/etc/systemd/system/certbot-renew-saas.timer <<'EOF'
[Unit]
Description=Twice-daily SaaS TLS renewal check
[Timer]
OnCalendar=*-*-* 03,15:00:00
RandomizedDelaySec=3600
Persistent=true
[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable --now certbot-renew-saas.timer
  systemctl list-timers certbot-renew-saas.timer --no-pager || true
else
  CRON_LINE="17 3,15 * * * root /usr/bin/certbot renew --quiet --deploy-hook \"docker compose -f $PROJECT_DIR/$COMPOSE_FILE exec -T nginx nginx -s reload\""
  if ! grep -qs "certbot renew" /etc/crontab; then
    echo "$CRON_LINE" >>/etc/crontab
  fi
  echo "cron entry installed."
fi

echo "Certificate live at /etc/letsencrypt/live/$DOMAIN/ -- run ./scripts/deploy_oracle.sh to cut over to the full HTTPS config."
