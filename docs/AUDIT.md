# Phase 1 — Production Deployment Audit Report

Date: 2026-09-13. Scope: deployability of the existing Go/Gin/GORM/Postgres/Redis
SaaS backend to Oracle Cloud Free Tier (2 OCPU/12GB) without redesigning the
architecture. Verdict: **the core was deployable; the edge was missing.** Every
gap below now has a fix in this repo (pointer in the last column).

## 1. Missing production configuration

| # | Gap | Fix |
|---|-----|-----|
| 1 | No production compose (dev file ran bare Postgres only) | `docker-compose.production.yml`: app+postgres+redis+nginx |
| 2 | No pool sizing for a small VM (defaults 25/5 risk `too many clients` under spike) | Phase 2 values: 20/5/30m + `REDIS_POOL_SIZE=20`, enforced in `config.Validate` |
| 3 | No `GIN_MODE=release`, no resource limits, no log rotation | Compose sets all three; json-file `10m×3` per service |
| 4 | No metrics, no admin analytics, no API docs | `/metrics`, `/api/v1/admin/overview`, `/swagger` (all zero new deps) |
| 5 | No CI/CD, no backups, no TLS automation | GHCR pipeline + SSH deploy + rollback; nightly dumps; certbot timer |

## 2. Security weaknesses (pre-existing code: strong; edges: fixed)

The core (RLS FORCE + NOBYPASSRLS role, JWT claim override, refresh rotation with
reuse detection, hash-only tokens/keys, append-only audit, layered upload caps)
audited **strong** — details in `docs/SECURITY_REVIEW.md`. Weaknesses were all at
the deployment edge: no TLS story, no rate limiting at the edge, query-string
tokens loggable, secrets loadable by default. All fixed (nginx TLS/rate-limit/
query-free logs, `${VAR:?}` fail-fast secrets, `.dockerignore` secret exclusion).

## 3. Deployment blockers (were)

- App bound `PORT` with no proxy story; dashboard+API same-origin (good) but no
  HTTPS/HSTS/gzip/rate-limit — **fixed** via nginx.
- `HEALTHCHECK` impossible in distroless (no shell/curl) — **fixed** via the
  `-healthcheck` binary flag + exec-form probes in Dockerfile and compose.
- Startup ordering (migrations before serving) — already handled in `main.go`
  (`MigrateAtStartup`); compose `service_healthy` gates now match it.

## 4. Oracle compatibility

- ARM Ampere: all images are multi-arch (`postgres/redis/nginx` 16/7/1.27-alpine,
  `golang:1.26-alpine`, distroless) — no x86-only dependency found (`go.mod`
  has no cgo beyond stdlib; `CGO_ENABLED=0`).
- OCI firewall is outside the VM: security-list ingress for 80/443 is a manual
  console step (documented) — the #1 gotcha for first-time OCI deploys.
- 12GB RAM budget: pg 2G + app 1G + redis 384M + nginx 256M + OS/cache ≈ 5GB —
  comfortable headroom, no swap needed.

## 5. Docker issues (were)

- `.dockerignore` shipped `.env`-only exclusion and bundled `data/`, docs, git
  into build context — **fixed** (secrets excluded, context slimmed).
- No `read_only` rootfs, no tmpfs, no OCI labels, no CA bundle guarantee —
  **fixed** in Dockerfile/compose (uploads volume remains the single writable path).

## 6. Database deployment risks

- `max_connections` default 100 vs unbounded-ish app pool — **bounded** (20) with
  the sizing rationale committed in `deploy/.env.production.example`.
- Migration credential vs runtime credential split already existed and is kept;
  compose wires both explicitly. New migration 000016 (`platform_stats()`)
  follows the sanctioned SECURITY DEFINER pattern — no RLS weakening.
- Backup story was absent — **fixed** (nightly `-Fc` + sha256 + retention + S3 +
  restore script + self-test flag).

## 7. Storage persistence risks

- Local uploads died with the container on departures from the named volume —
  compose now mounts persistent `uploads` volume at `/data/uploads`, seeded
  nonroot-owned; `STORAGE_ROOT` absolute-path enforcement already in
  `config.ValidateStorage`. Drift (orphan bytes / missing bytes) covered by
  `scripts/cleanup_uploads.sh` + metrics divergence signal
  (`saas_storage_bytes` vs `saas_storage_disk_used_bytes`).

## 8/9. Monitoring & observability (were absent)

- **Fixed:** `/metrics` (Prometheus text: requests/latency, cache hit-rate,
  domain counts, storage, DB pool, SSE clients), `/health/*` already existed and
  is now edge-proxied, admin overview for product-level analytics, structured
  request IDs end-to-end (Gin `request_id` → nginx `$request_id` → `meta.request_id`).
- Still deliberately absent (documented): distributed tracing, log aggregation —
  disproportionate for a single VM; request IDs + rotated json logs suffice.
