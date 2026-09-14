# Security Review — findings, severity, and fixes

Scope: JWT/refresh/API keys, RLS/RBAC/tenant isolation, uploads, audit logs,
plus the new surfaces added in this production pass (metrics, admin, SSE, nginx).
Each finding states severity, risk, and either the fix applied or the accepted
risk with compensating controls. Nothing is rated "fixed" without a code/config
pointer.

## F1. SSE query-string tokens persisted in access logs — FIXED (Medium)

- **Risk:** `GET /notifications/stream?access_token=` exists for EventSource
  clients. Default nginx logging (`$request`) records the query string, writing
  bearer tokens to `/var/log/nginx/access.log`.
- **Fix:** `deploy/nginx/nginx.conf` logs `$uri` instead of `$request` — query
  strings never reach disk. Header auth stays the documented default
  (`internal/realtime/sse.go:QueryTokenAuth` doc).
- **Residual:** tokens remain in transit (TLS) and browser history; acceptable
  for a 15-minute JWT, and refresh tokens are never accepted there.

## F2. No shared rate-limit state across app instances — ACCEPTED (Low, single VM)

- **Risk:** `middleware.NewIPRateLimiter` (20/min, burst 5, in-process) does not
  synchronize across replicas; N replicas allow N× the rate.
- **Compensating control:** nginx `limit_req` zones (`auth: 10r/m`, `api: 100r/m`)
  enforce limits at the single edge before traffic fans out — this is the layer
  that actually matters on one VM. Documented for the multi-instance future
  (Redis-backed limiter), not implemented because there is no second instance.

## F3. Client-supplied Content-Type stored and served back — MITIGATED (Medium)

- **Risk:** upload stores the uploader's `Content-Type`; serving it back inline
  would turn stored SVG/HTML into stored XSS in the victim's origin.
- **Existing control (verified):** downloads use
  `Content-Disposition: attachment` (`internal/files/handler.go`), so the
  browser downloads instead of rendering — the XSS sink is closed. Storage keys
  are opaque (`tenant/file-uuid`, never the user filename), blocking path
  traversal + MIME confusion by extension.
- **Recommendation (not implemented, documented):** serve with
  `X-Content-Type-Options: nosniff` on downloads (already global via middleware
  for API/HTML; add to the file-download path if inline preview is ever added)
  and optional ClamAV sidecar for malware — out of scope for a 2-OCPU VM.

## F4. JWT secret rotation invalidates all sessions — ACCEPTED (Low)

- **Risk:** HS256 with one shared secret (`JWT_SECRET`, ≥32 chars enforced at
  startup); rotation logs everyone out. No `kid`/key-version scheme.
- **Why accepted:** single-issuer monolith — asymmetric JWT buys nothing without
  a second verifier; short access TTL (15m) bounds the blast radius of a leak,
  and refresh-token family reuse detection (`internal/identity/service.go`)
  revokes whole families on theft signals. Rotation runbook: set new secret,
  `compose restart app`, users re-login (documented in INTERVIEW.md).

## F5. Refresh-token design — VERIFIED STRONG

- Opaque 256-bit tokens, SHA-256 hash stored only; rotation with family reuse
  detection revokes the family on replay (theft signal); logout revokes family.
  No finding.

## F6. API keys — VERIFIED STRONG

- `sk_live_` prefix + 256-bit secret, hash-only storage, one-time plaintext
  display; scopes exclude role/org administration (`GrantableScopes`), so a
  leaked key cannot escalate to owner; minting requires a user session
  (`RequireUserSession`), so a key cannot mint its own replacement. Expiry
  encouraged, revocation immediate. No finding.

## F7. RLS / tenant isolation — VERIFIED STRONG (core guarantee)

- `FORCE ROW LEVEL SECURITY` on all tenant tables; policy
  `tenant_id = current_setting('app.tenant_id', true)::uuid` fails closed
  (NULL comparison hides rows when unset).
- Runtime credential is `NOSUPERUSER NOBYPASSRLS` (`scripts/provision_app_role.sql`,
  with a startup guard that aborts provisioning if either flag is set).
- Every repository passes through `pkg/txscope` (`SET LOCAL`, auto-reset per
  transaction — no cross-connection leakage on pooled conns).
- Post-auth, the JWT `tenant_id` claim **overrides** any `X-Tenant-ID`/subdomain
  hint and mismatches are rejected (`identity.RequireAuth` + `tenancy.OverrideFromCredential`)
  — token replay across tenants is structurally impossible.
- New code follows the same rules: admin overview runs in `WithTenantTxID` for
  the caller's tenant (no cross-tenant path exists); platform totals come from
  the aggregate-only `platform_stats()` SECURITY DEFINER function (counts, no PII),
  following the sanctioned migrations/000011-12 pattern.
- **Operational caveat:** anyone with the `postgres` (migration) credential or
  direct DB access bypasses RLS — that credential lives only in `.env.production`
  (0600, ubuntu-only) and never in git/CI logs. `docker-compose.production.yml`
  fails fast if it is missing rather than defaulting to something weak.

## F8. RBAC permission checks — VERIFIED SOUND

- Token `role` claim is display-only; every gate re-verifies against DB/cache
  (`authzService.RequirePermission`), so role changes take effect before token
  expiry. Permission cache TTL (5m) bounds staleness; mutations invalidate
  explicitly. No finding.

## F9. Audit logs — VERIFIED APPEND-ONLY

- No update/delete code path; DB role lacks UPDATE/DELETE grants
  (`REVOKE ... ON audit_logs`, re-applied by `restore_postgres.sh`).
  Actor/IP/user-agent recorded per security-relevant action. 30-day volume
  visible in metrics/admin for retention monitoring. No finding.

## F10. Upload size enforcement — VERIFIED LAYERED

- nginx `client_max_body_size 30m` rejects oversize bodies before Go sees them;
  handler wraps the body in `http.MaxBytesReader` (+1MB multipart headroom);
  service checks plan storage quota *before* writing bytes. Three layers, same
  limit family (`MAX_UPLOAD_BYTES=25MB`). No finding.

## F11. /metrics exposure — CONTROLLED (Low)

- Serves aggregate counts only (no tenant PII, no row contents); DB-pool stats
  and disk usage are capacity data, safe to expose to operators.
- Optional `METRICS_TOKEN` bearer gate (401 without it when configured) +
  documented nginx `allow/deny` snippet for internet-facing Prometheus.
- Default (blank token) is open — correct behind the private backend network
  and in-network scraping; the deploy checklist prompts the operator to set it
  when the endpoint is public.

## F12. Secrets handling — VERIFIED, one rule restated

- `.dockerignore` excludes every `.env*` variant (verified: image contains no
  env file); compose `${VAR:?message}` fails fast on missing secrets instead of
  starting degraded; `DB_PASSWORD ≠ POSTGRES_PASSWORD` enforced by the deploy
  script; `.env.production` is `chmod 600` (deploy script warns otherwise —
  see hardening note below).
- CI: secrets travel as GitHub Secrets → SSH env only; GHCR pull on the VM uses
  a PAT file (`~/.ghcr_pat`), never committed.

## Hardening applied in this pass (summary)

| Area | Control |
|------|---------|
| Image | distroless static, nonroot, no shell/curl, CA bundle + tzdata, OCI labels, exec-form HEALTHCHECK via `-healthcheck` flag |
| Runtime | `read_only: true` + tmpfs /tmp; only `/data/uploads` writable |
| Edge | TLS-only, HSTS, CSP/X-Frame/X-Content-Type headers, gzip, per-zone rate limits, 30m body cap, SSE `proxy_buffering off`, 1h stream timeouts, query-free access logs |
| Data | RLS FORCE + NOBYPASSRLS role, nightly `-Fc` dumps + sha256 + 7-day retention + optional S3 + restore self-test, orphan upload janitor |
| Supply | Pinned base images (`postgres:16-alpine`, `redis:7-alpine`, `nginx:1.27-alpine`), GHCR image per deploy SHA, rollback-to-`:stable` in CI |
