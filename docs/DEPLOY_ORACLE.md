# Oracle Cloud Free Tier — Production Deployment Guide

Target: Ubuntu 24.04 on an Ampere A1 Flex instance (2 OCPU, 12 GB RAM, Always Free).
Result: `https://YOUR_DOMAIN` serving the API + dashboard behind nginx with TLS,
plus Postgres, Redis, backups, and renewal — all from one command.

Time: ~45 minutes the first time (mostly OCI console + DNS propagation).

---

## 0. Architecture on the VM (what you are about to run)

```
                    ┌──────────────────────────────────────────────┐
                    │ Oracle VM (2 OCPU / 12 GB)                    │
                    │                                               │
Internet ──:80/443─▶│ nginx (TLS, gzip, rate-limit, 30m uploads)    │
                    │   │ proxy_pass (frontend net)                 │
                    │   ▼                                           │
                    │ Go/Gin app :8080 (distroless, nonroot, ro-fs) │
                    │   │ (backend net, internal — no gateway)      │
                    │   ├─▶ postgres:5432 (RLS, max_conn=100)       │
                    │   └─▶ redis:6379 (256MB, allkeys-lru)         │
                    │                                               │
                    │ volumes: pgdata · redisdata · uploads         │
                    └──────────────────────────────────────────────┘
```

Only nginx publishes host ports. Postgres/Redis sit on an `internal:true`
network — even a bad port mapping cannot expose them; the network has no gateway.

## 1. Create the VM (OCI console)

1. Compute → Instances → Create instance, image **Ubuntu 24.04**, shape **VM.Standard.A1.Flex**
   (Ampere ARM — Always Free eligible), OCPU **2**, memory **12 GB**.
2. Add your SSH public key. Note the public IP.
3. **Subnet security list** (VCN → Security Lists → ingress): add
   `0.0.0.0/0 → TCP 80` and `0.0.0.0/0 → TCP 443` (keep 22 restricted to your IP).
   The OCI firewall is *outside* the VM — opening ports in iptables alone is not enough.
4. DNS: create A record `api.example.com → <VM public IP>`. TLS issuance needs
   this resolving to the VM (verify: `dig +short api.example.com`).

## 2. Prepare the VM (~10 min)

```bash
ssh ubuntu@<VM-IP>

# --- OS firewall: allow web, keep everything else closed ---
sudo iptables -I INPUT 6 -p tcp --dport 80 -j ACCEPT
sudo iptables -I INPUT 6 -p tcp --dport 443 -j ACCEPT
sudo netfilter-persistent save   # apt install iptables-persistent if missing

# --- Docker (official repo; includes the compose plugin) ---
sudo apt-get update -y
sudo apt-get install -y ca-certificates curl git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
  https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list
sudo apt-get update -y
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
sudo usermod -aG docker $USER
newgrp docker   # or log out/in

# --- Repo ---
git clone <YOUR-REPO-URL> ~/tenant-saas-backend
cd ~/tenant-saas-backend
cp deploy/.env.production.example .env.production
nano .env.production   # fill DOMAIN + every CHANGE-ME (openssl rand -base64 32)
```

Generate secrets properly:
```bash
openssl rand -base64 32   # JWT_SECRET (x1)
openssl rand -base64 24   # DB_PASSWORD, POSTGRES_PASSWORD, REDIS_PASSWORD (x3, all different)
```

## 3. Deploy (one command)

```bash
cd ~/tenant-saas-backend
./scripts/deploy_oracle.sh
```

What it does, in order: validates secrets → renders nginx config →
starts postgres+redis and waits for health → issues the Let's Encrypt cert
via webroot (first run) → builds/starts the app (embedded migrations run
before serving) → cuts nginx over to full HTTPS → validates every public
endpoint and fails loudly if any check fails.

Expected end of output: `Deploy complete.` with the App/Health/Metrics/Swagger URLs.

## 4. Post-deploy one-time setup

```bash
# Nightly DB backup, 7-day retention (Phase 13)
(crontab -l 2>/dev/null; echo "17 3 * * * $HOME/tenant-saas-backend/scripts/backup_postgres.sh") | crontab -

# Weekly upload janitor: orphan report is default; --delete removes orphans >24h
(crontab -l 2>/dev/null; echo "30 4 * * 0 $HOME/tenant-saas-backend/scripts/cleanup_uploads.sh --delete") | crontab -

# TLS renewal: installed automatically by scripts/ssl_setup.sh
# (systemd timer certbot-renew-saas.timer, or cron fallback). Verify:
sudo systemctl list-timers certbot-renew-saas.timer
```

Backups also need a home: `BACKUP_DIR` defaults to `/var/backups/saas-postgres`
(create it, owned by ubuntu), and set `S3_DEST=s3://…` in the cron environment
for the off-VM copy. Test a restore into a scratch DB quarterly
(`scripts/restore_postgres.sh <dump> restore_verify_$(date +%F)`).

## 5. Validation checklist (run after every deploy)

| # | Check | Command / URL |
|---|-------|---------------|
| 1 | HTTPS landing | `curl -s -o /dev/null -w '%{http_code}' https://$DOMAIN/` → 200 |
| 2 | Readiness (DB reachable) | `https://$DOMAIN/health/ready` → `{"status":"ready"}` |
| 3 | HTTP→HTTPS redirect | `curl -s -o /dev/null -w '%{http_code}' http://$DOMAIN/health` → 301 |
| 4 | Security headers | `curl -sI https://$DOMAIN/ \| grep -i strict-transport` present |
| 5 | Metrics | `https://$DOMAIN/metrics` contains `saas_api_requests_total` |
| 6 | Swagger | `https://$DOMAIN/swagger` → 200 |
| 7 | Auth round-trip | register → login → `GET /api/v1/me` → 200 |
| 8 | Tenant isolation probe | login as org A, request with `X-Tenant-ID` of org B → 403/tenant-mismatch |
| 9 | Uploads persist | upload file → `docker compose restart app` → download still works |
| 10 | SSE stream | `curl -N -H "Authorization: Bearer <token>" https://$DOMAIN/api/v1/notifications/stream` → `event: ready` |
| 11 | Backup exists | `ls -la /var/backups/saas-postgres/ \| tail -3` (after 03:17 UTC) |
| 12 | Cert auto-renewal | `sudo certbot renew --dry-run` succeeds |
| 13 | Resource headroom | `docker stats --no-stream` — app <1G, pg <2G, redis <384M |
| 14 | Admin overview | `GET /api/v1/admin/overview` (org:view) → counts match dashboard |

## 6. Operations cheat sheet

```bash
cd ~/tenant-saas-backend
docker compose -f docker-compose.production.yml --env-file .env.production ps
docker compose -f docker-compose.production.yml --env-file .env.production logs --tail=100 app
docker compose -f docker-compose.production.yml --env-file .env.production restart app
docker compose -f docker-compose.production.yml --env-file .env.production exec -T app /tenant-saas -healthcheck
./scripts/backup_postgres.sh            # manual backup now
./scripts/cleanup_uploads.sh --report   # storage audit (read-only)
```

## 7. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `deploy_oracle.sh` fails at TLS | DNS not pointing at VM, or OCI security list missing 80/443 | `dig +short $DOMAIN`; check VCN security list (not just iptables) |
| App CrashLoop, `JWT_SECRET` error | Placeholder secret still in `.env.production` | Fill all CHANGE-ME; secrets must differ |
| `database unreachable` in /ready | Postgres still initializing on first boot | Wait 60s; `logs postgres`; volume `saas_pgdata` persists it |
| 413 on uploads | File > MAX_UPLOAD_BYTES | Raise both `MAX_UPLOAD_BYTES` and nginx `client_max_body_size` together |
| SSE connects but no events | `proxy_buffering off` missing (custom nginx edit?) | Use the shipped config; check `X-Accel-Buffering: no` header present |
| High memory (OOM) | Defaults are sized for 12GB; smaller shape? | Lower `shared_buffers` to 128MB and app limit to 512M |
| Cert expired despite timer | `certbot-renew-saas.timer` inactive | `sudo systemctl enable --now certbot-renew-saas.timer`; check `journalctl -u certbot-renew-saas` |
