# Tenant SaaS Backend

Multi-tenant SaaS API and workspace dashboard built with Go, Gin, GORM, and
PostgreSQL. One application instance and one database serve many tenants, with
isolation enforced at the database level rather than only in application code.

This README is written as a system design document with GitHub-rendered diagrams
(Mermaid), so the architecture is visible directly on the repository page.

## 1) What This Project Does

`Tenant-Saas-Backend` is a complete organization-management backend: the layer
every B2B SaaS needs before it can build its actual product.

| Capability | What it covers |
|---|---|
| Tenant onboarding | Create an organization plus its first owner atomically |
| Authentication | Password login, refresh-token rotation with theft detection, password reset, email verification, Google/GitHub OAuth |
| Authorization | Database-driven RBAC: 5 seeded roles, 21 permissions, editable per tenant |
| Tenant isolation | PostgreSQL Row-Level Security on every tenant-scoped table |
| Teams & projects | Grouping, membership, archival, soft delete |
| Invitations | Single-use hashed invite tokens redeemable without a prior account |
| API keys | Scoped machine credentials, hashed at rest, revocable |
| Billing | Plan catalog, per-tenant subscription, enforced seat/project quotas |
| Notifications | Per-user in-app feed with unread tracking |
| Audit & activity | Append-only compliance log plus a product-facing timeline |
| Self-service | Profile, display preferences, password rotation |
| Workspace UI | A dashboard for all of the above, embedded in the binary |

## 2) High-Level Architecture

Each domain is a self-contained module under `internal/`, following the same
four-file shape. Handlers never touch the database and repositories never make
authorization decisions.

```mermaid
flowchart TB
      C[Client: browser dashboard, API consumer, or CI pipeline]

      subgraph MW[Middleware chain]
        direction TB
        RID[RequestID] --> LOG[Structured logger]
        LOG --> REC[Panic recovery]
        REC --> SEC[Security headers + CORS]
        SEC --> TEN[Tenant resolver]
        TEN --> AUTH[API key auth / JWT auth]
        AUTH --> PERM[Permission gate]
      end

      C --> MW

      subgraph MOD[Domain modules: handler to service to repository]
        direction LR
        M1[identity]
        M2[authz]
        M3[teams / projects]
        M4[invitations / apikeys]
        M5[billing]
        M6[notifications / audit / preferences]
      end

      PERM --> MOD
      MOD --> TX[txscope: sets app.tenant_id]
      TX --> PG[(PostgreSQL + RLS policies)]
```

## 3) Request Lifecycle

```mermaid
sequenceDiagram
      participant U as Client
      participant MW as Middleware
      participant H as Handler
      participant S as Service
      participant TX as txscope
      participant PG as PostgreSQL

      U->>MW: GET /api/v1/projects + credential
      MW->>MW: Assign request ID
      MW->>MW: Validate JWT or API key
      MW->>MW: Set tenant from the credential (not the header)
      MW->>MW: Check project:view permission or key scope
      MW->>H: Authorized request
      H->>S: List(ctx, filter, page)
      S->>TX: Repository call
      TX->>PG: BEGIN; SELECT set_config('app.tenant_id', ...)
      TX->>PG: SELECT * FROM projects
      Note over PG: RLS restricts rows to this tenant<br/>even without a WHERE clause
      PG-->>H: Tenant-scoped rows
      H-->>U: {success, data, meta.request_id}
```

## 4) Tenant Isolation Model

Isolation is enforced in three independent layers, so a mistake in one does not
become a data leak.

```mermaid
flowchart TD
      R[Incoming request] --> A{Credential valid?}
      A -- No --> X401[401]
      A -- Yes --> B[Derive tenant_id from the credential]
      B --> C{Header/subdomain tenant disagrees?}
      C -- Yes --> X403[403 TENANT_MISMATCH]
      C -- No --> D[Permission or scope check]
      D --> E[txscope opens a transaction<br/>and sets app.tenant_id]
      E --> F[RLS policy filters every row]
      F --> G[Tenant-scoped response]
```

Design rules:

- Tenant identity comes only from a verified credential. A pre-auth
  `X-Tenant-ID` header or subdomain is used solely to route unauthenticated
  pages, and is rejected if it contradicts the credential.
- Every tenant-scoped query goes through `pkg/txscope`, which sets
  `app.tenant_id` inside a transaction. `SET LOCAL` semantics mean the value
  cannot leak across pooled connections.
- The application connects as a `NOSUPERUSER NOBYPASSRLS` role. Postgres
  superusers ignore RLS entirely, so running as one would silently disable the
  guarantee. See `scripts/provision_app_role.sql`.
- Three lookups genuinely cannot know the tenant in advance (invite tokens,
  API keys, login by email). Rather than weakening RLS, each is exposed through
  a narrowly scoped `SECURITY DEFINER` function that performs one indexed
  equality match on a high-entropy secret.

## 5) Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.26 |
| HTTP framework | Gin |
| ORM | GORM + pgx driver |
| Database | PostgreSQL 16 (RLS, CITEXT, JSONB, triggers) |
| Migrations | golang-migrate, embedded via `go:embed` |
| AuthN | JWT (HS256) access tokens + rotating opaque refresh tokens |
| AuthZ | Database-driven RBAC; API key scopes |
| Password hashing | bcrypt |
| Token/key hashing | SHA-256 (high-entropy secrets only) |
| Logging | `log/slog`, JSON in production |
| Frontend | Embedded dependency-free SPA |
| Local infra | Docker Compose |

## 6) Project Structure

```text
cmd/server/main.go               Entrypoint: timeouts + graceful shutdown
cmd/migrate/main.go              Migration CLI (up / down / version)

internal/config/                 Environment configuration + production validation
internal/db/                     Connection pool, startup ping, migration runner
internal/middleware/             RequestID, slog logger, recovery, security headers, CORS, rate limit
internal/platform/               Health / readiness / liveness endpoints
internal/eventbus/               Domain event publisher interface (no-op until Kafka)

internal/tenancy/                Tenant entity, resolver, credential-based tenant override
internal/identity/               Users, login, refresh rotation, password reset, OAuth
internal/authz/                  Roles, permissions, permission gates (user roles or key scopes)
internal/teams/                  Teams and team membership
internal/projects/               Projects, membership, archival, plan quota check
internal/invitations/            Hashed invite tokens, preview, atomic accept
internal/apikeys/                Scoped machine credentials + authentication middleware
internal/billing/                Plan catalog, subscriptions, seat/project quota enforcement
internal/notifications/          Per-user in-app notifications
internal/audit/                  Append-only audit log + activity feed
internal/preferences/            Self-service profile, preferences, password change

internal/routes/routes.go        Dependency injection root and route table
internal/routes/web/             Embedded landing page + dashboard (landing.html, index.html, assets)

pkg/apperror/                    Typed domain errors mapped to HTTP status
pkg/apiresponse/                 {success, data, meta} envelope + pagination
pkg/txscope/                     The only path to tenant-scoped database access
pkg/reqctx/                      Caller extraction and error translation for handlers
pkg/dberr/                       SQLSTATE to error-code classification
pkg/slug/                        URL-safe slug generation

migrations/                      Versioned SQL, embedded into the binary
scripts/provision_app_role.sql   Creates the low-privilege runtime role
```

Every domain module uses the same four files: `entity.go` (tables), `repository.go`
(data access via `txscope`), `service.go` (business rules, returns typed errors),
`handler.go` (Gin binding only).

## 7) API Surface

All responses use one envelope: `{success, data, meta}` or `{success, error, meta}`.
`meta.request_id` matches the `X-Request-ID` response header and the server logs.

### Public

| Method | Path | Purpose |
|---|---|---|
| GET | `/health`, `/health/ready`, `/health/live` | Probes |
| POST | `/api/v1/auth/register` | Create organization + owner |
| POST | `/api/v1/auth/login` | Authenticate; may ask to disambiguate tenant |
| POST | `/api/v1/auth/refresh` | Rotate token pair |
| POST | `/api/v1/auth/logout` | Revoke a token family |
| POST | `/api/v1/auth/forgot-password`, `/reset-password` | Password reset |
| POST | `/api/v1/auth/verify-email` | Consume verification token |
| GET | `/api/v1/auth/oauth/:provider`, `/:provider/callback` | OAuth login |
| GET | `/api/v1/invitations/preview?token=` | Inspect an invite pre-signup |
| POST | `/api/v1/invitations/accept` | Redeem an invite |
| GET | `/api/v1/billing/plans` | Plan catalog |

Unauthenticated endpoints are rate-limited to 20 requests/minute per IP with a
burst of 5.

### Authenticated

Permission in brackets. API keys authorize from their scopes instead of roles.

| Method | Path | Permission |
|---|---|---|
| GET | `/api/v1/me`, `/profile`, `/preferences` | — (own account) |
| PATCH | `/api/v1/profile`, `/preferences` | — (user session only) |
| POST | `/api/v1/profile/change-password` | — (user session only) |
| POST | `/api/v1/verify-email/request` | — (own account) |
| GET | `/api/v1/notifications`, `/unread-count` | — (own notifications) |
| POST | `/api/v1/notifications/read-all`, `/:id/read` | — |
| GET | `/api/v1/users` | `member:view` |
| GET/POST/PUT/DELETE | `/api/v1/roles…` | `role:view` / `role:manage` |
| GET | `/api/v1/permissions` | `role:view` |
| GET/POST | `/api/v1/teams` | `team:view` / `team:create` |
| PATCH/DELETE | `/api/v1/teams/:teamID` | `team:manage` |
| GET/POST/DELETE | `/api/v1/teams/:teamID/members…` | `team:view` / `team:manage` |
| GET/POST | `/api/v1/projects` | `project:view` / `project:create` |
| PATCH | `/api/v1/projects/:projectID` | `project:manage` |
| DELETE | `/api/v1/projects/:projectID` | `project:delete` |
| GET/POST/DELETE | `/api/v1/projects/:projectID/members…` | `project:view` / `project:manage` |
| GET/POST | `/api/v1/invitations` | `member:view` / `member:invite` |
| DELETE | `/api/v1/invitations/:inviteID` | `member:invite` |
| GET | `/api/v1/api-keys` | `apikey:view` |
| POST/DELETE | `/api/v1/api-keys…` | `apikey:manage` + user session |
| GET | `/api/v1/billing/subscription`, `/usage` | `billing:view` |
| POST/DELETE | `/api/v1/billing/subscription` | `billing:manage` |
| GET | `/api/v1/audit-logs` | `audit:view` |
| GET | `/api/v1/activity` | `org:view` |

### Example: register

```json
{
  "tenant_name": "Acme Inc",
  "tenant_slug": "acme",
  "email": "admin@acme.com",
  "password": "supersecret123",
  "full_name": "Ada Lovelace"
}
```

### Credentials

```http
Authorization: Bearer <jwt-access-token>
```

```http
X-API-Key: sk_live_...
```

## 8) Authorization Model

Five roles are seeded per tenant by a database trigger and marked
`is_system`. Their permission sets are editable; their slugs are not, because
several flows match on `owner` specifically.

| Role | Rank | Default grants |
|---|---|---|
| Owner | 0 | Everything, including `org:manage` |
| Admin | 10 | Everything except `org:manage` |
| Manager | 20 | Teams, projects, invites, no billing or role management |
| Member | 30 | View, create projects, upload files |
| Guest | 40 | Read-only |

Permission checks resolve from the database on every request, so a role edit
takes effect immediately without a redeploy.

API keys are authorized purely from their own scope list. The key's creator is
recorded for attribution but never consulted for authority, so a key neither
gains capability when its owner is promoted nor keeps it after they are demoted.
`role:manage` and `org:manage` cannot be granted to a key at all, and keys are
blocked from minting keys or changing passwords.

## 9) Local Setup

```bash
cp .env.example .env

make db-up                   # start PostgreSQL
make migrate-up              # apply migrations (uses MIGRATE_DB_* credentials)
make db-provision-app-role   # create the low-privilege app_user role
make run                     # start the server
```

Open `http://localhost:8080` for the landing page, or go straight to
`http://localhost:8080/app` and create a workspace. Both are served from the same
origin as the API, so no CORS configuration is needed.

The two-credential split matters: migrations need privileges (`CREATE POLICY`,
`CREATE ROLE`) that the runtime role deliberately lacks. Pointing `DB_USER` at a
superuser would make tenant isolation tests pass for the wrong reason.

## 10) Development Commands

```bash
make build              make test               make test-verbose
make vet                make fmt                make tidy
make db-up              make db-down
make migrate-up         make migrate-down       make migrate-version
make db-provision-app-role
```

Tests skip automatically when no database is reachable, so `go test ./...` stays
usable without Docker. With a database available, the integration suite in
`internal/routes/` exercises the real router against real RLS policies:
cross-tenant reads, plan quota rejection, invite redemption, API key scope
enforcement, and audit capture.

When changing the dashboard JavaScript, run `node --check
internal/routes/web/assets/app.js`; it is not covered by `go vet`. The same
applies to `landing.js` and `theme.js`.

The stylesheet *is* covered. `TestInkRampMeetsWCAGAA` parses `app.css`, resolves
each ink token through the neutral ramp for both schemes, and computes the real
WCAG 2.1 contrast ratio against the backgrounds the token is painted on, failing
below 4.5:1. `TestDarkSchemeBlocksAgree` asserts the two dark blocks — the
`prefers-color-scheme` one and the `:root.theme-dark` override — never drift
apart on any declaration, since the file has to declare the whole dark scheme
twice. Both read from the embedded filesystem the server serves from, so neither
can pass against a stale copy.

## 11) Security Notes

- Set a strong `JWT_SECRET` (32+ characters). The server refuses to start in
  production without one.
- Run as a `NOSUPERUSER NOBYPASSRLS` database role, or RLS is silently inert.
- Passwords use bcrypt. Invite tokens and API keys use SHA-256, which is correct
  only because they are 256 bits of uniform randomness rather than guessable
  secrets; they are also verified on hot paths where a slow KDF would dominate.
- Secrets are shown exactly once at creation and stored only as hashes. They are
  never written to audit entries or logs.
- Changing a password revokes every refresh token for that user.
- `audit_logs` is append-only: the application role holds no `UPDATE` or
  `DELETE` grant, so even a fully compromised credential cannot erase the trail.
- Responses carry `Content-Security-Policy`, `X-Frame-Options: DENY`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy`, and `Permissions-Policy`.
  HSTS is added only over TLS.
- CORS is disabled by default and has no wildcard mode, since responses are
  credential-bearing.
- The rate limiter is in-memory per instance. For multiple instances, move it to
  shared storage such as Redis.
- Request logs deliberately omit bodies, query strings, and the `Authorization`
  header, so credentials never reach log storage.

## 12) Web Workspace

Two documents ship inside the binary, with no separate frontend build, static
host, or CORS boundary. `/` is the landing page; `/app` is the dashboard, which
covers overview and quota usage, projects, teams, members and invitations, roles
and permissions, API keys, billing, the audit log, and account settings.

The landing page has no design system of its own — it loads `app.css` for the
token layer and shared primitives and adds only `landing.css` on top, so the two
cannot drift apart visually. One rule governs its layout: a frame is something a
section has to earn by being a surface you could point at. The product figure,
the lifecycle panel, the code block and the plan cards are bordered; every other
section is built from rules, indents and type. An earlier version framed eight
consecutive sections identically, and the effect was that the reader stopped
seeing sections and started seeing a template. Motion follows the same logic:
the default reveal translates, but ruled sections instead draw their own rule
along its axis and only fade the type, so the animation is the section's device
performing itself rather than a generic entrance applied to everything.

Two of its sections are genuinely live rather than illustrative: the hero status
pill measures a real request to `/health` and reports the failure if there is
one, and the pricing cards are rendered from `GET /api/v1/billing/plans`, so the
limits shown are the limits the server enforces. The isolation section animates
the real middleware chain with sample rows, and says so. `landing.js` also
forwards legacy `/#/route` bookmarks to `/app`, since the workspace used to live
at `/`.

Four tests in `web_landing_test.go` turn that file's header rules into build
failures: no `innerHTML` anywhere (the plan cards render API data), no inline
`style` attribute (`style-src 'self'` blocks it, so it would fail only behind the
real server), every `#id` the script looks up exists in the markup (each lookup
fails soft, so a rename would leave a section silently inert), and `landing.css`
declares neither a `prefers-color-scheme` block nor a px `font-size`.

Implementation constraints worth knowing before editing it:

- No dependencies, no build step, no framework.
- All DOM is constructed through an `el()` helper that assigns text via
  `textContent`. There is no `innerHTML` anywhere, because tenant-supplied names
  and audit metadata would otherwise be a stored-XSS vector. `el()` throws if
  handed an `html` key.
- The CSP forbids inline styles, so `el()` also throws on a `style` key; spacing
  uses utility classes. Programmatic `element.style.width` (the quota bars) is
  fine, as CSSOM writes are not CSP-restricted.
- Fonts are system stacks. No third-party origin is contacted at runtime.
- Motion is CSS keyframes and transitions only, on `transform` and `opacity`.
  A JS animation library is not an option here rather than a preference: they
  animate by writing inline styles, which `style-src 'self'` blocks, so adopting
  one would mean weakening a real security control. Entrances are pure CSS — a
  freshly built element plays its animation on first render, and every render in
  `app.js` builds new nodes. Three exits need JS, because a `<dialog>` leaves the
  top layer the frame `close()` runs, a toast leaves the DOM, and the drawer
  scrim is toggled with `[hidden]`: `app.js` adds `.is-closing`, waits, then
  performs the removal, so the `EXIT` table there and the duration tokens in
  `app.css` describe the same intervals and must change together. Everything is
  neutralised by the `prefers-reduced-motion` block, which needs an explicit
  `*::backdrop` rule — the universal selector does not reach a pseudo-element,
  and the backdrop is the one animation that covers the whole viewport.
- Colour is never the only signal, and the ink ramp is contrast-tested in CI
  (see section 10). A `forced-colors` block at the end of `app.css` handles
  Windows High Contrast Mode, where the browser drops `box-shadow` — which would
  otherwise erase the focus ring on `.btn` and `.icon-btn`, since both set
  `outline: none` and draw their ring as a shadow.
- Access tokens live in `sessionStorage` and clear with the tab. For a
  persistent cross-device session, add HttpOnly cookie auth at the API layer.
- The UI hides controls the caller lacks permission for, purely to avoid dead
  ends. Every action is still authorized server-side.

Client-side routes fall back to the SPA document so a refresh on `/projects`
works, while unknown `/api/` paths still return a JSON 404.

## 13) Production Container Deployment

The `Dockerfile` produces a minimal non-root image containing the API and the
dashboard.

```bash
docker build -t tenant-saas:latest .
docker run --rm -p 8080:8080 \
  -e APP_ENV=production \
  -e DATABASE_URL='postgresql://app_user:secret@your-host:5432/tenant_saas?sslmode=require' \
  -e JWT_SECRET='use-a-long-random-secret' \
  tenant-saas:latest
```

For an all-container preview deployment on a private VM:

```bash
docker compose -f docker-compose.production.yml up -d --build
```

Do not expose PostgreSQL publicly. Put the application behind a TLS-terminating
proxy, set `DB_SSLMODE=require` for remote databases, and keep `JWT_SECRET` in a
secret manager. Set `SHUTDOWN_TIMEOUT` below your orchestrator's termination
grace period so in-flight requests drain rather than being severed.

## 14) Render Deployment

`render.yaml` defines a web service plus a same-region Render Postgres database,
wires the database's private `connectionString` into `DATABASE_URL`, and asks
Render to generate `JWT_SECRET`. Neither secret is committed. `DATABASE_URL`
takes precedence over the individual `DB_*` settings. The health check is
`GET /health`.

Both services run on Render's paid tiers, deliberately. The free tier is
unsuitable for a real deployment: a free Postgres instance expires 30 days after
creation and is then deleted, and a free web service sleeps after 15 minutes of
inactivity and cold-starts on the next request.

The blueprint also sets production-relevant runtime configuration: pool limits
(`DB_MAX_OPEN_CONNS=20`, kept under the instance's connection ceiling), token
lifetimes, and `SHUTDOWN_TIMEOUT=20s` so in-flight requests drain on deploy. The
database's `ipAllowList` is empty, which keeps it off the public internet and
reachable only over Render's private network.

Deploying:

1. Push the repository, then in Render choose **New → Blueprint** and select it.
2. Render provisions the web service and the database, generates `JWT_SECRET`,
   and injects `DATABASE_URL`. No manual environment entry is required.
3. Migrations apply automatically at startup (`cmd/server` runs them before
   serving), so there is no separate migration step or shell access needed.

To fix an existing Render service without recreating it:

1. Create a Render Postgres instance in the same region as the web service.
2. Set `DATABASE_URL` to the database's **Internal Database URL**.
3. Set `JWT_SECRET` to a new random value of at least 32 characters.
4. Set `APP_ENV=production` and remove stale `DB_HOST`/`DB_PORT`/`DB_USER`/
   `DB_PASSWORD`/`DB_NAME` overrides, then deploy.

The server refuses to start in production with a missing or short JWT secret, or
an unconfigured database, producing an actionable configuration error instead of
silently trying `localhost:5432`.

One subtlety worth knowing on managed providers: Render hands you a database user
that **owns** the tables, and a table owner normally bypasses its own RLS
policies. Isolation still holds here only because every tenant-scoped table is
declared `FORCE ROW LEVEL SECURITY`, which applies the policy to the owner too.
If you add a new tenant-scoped table, it needs both `ENABLE` and `FORCE`, or that
one table will silently leak across tenants on exactly this kind of deployment.

## 15) Not Yet Implemented

Interfaces exist and degrade to no-ops, so these can be added without touching
call sites:

- **Email delivery.** Invitation, password-reset, and verification tokens are
  published as domain events but not sent. Until a transport is wired up, the
  dashboard surfaces a new invite link directly so the flow is usable.
- **Kafka event publishing** (`internal/eventbus` is a no-op publisher).
- **Redis caching** for permissions and tenant metadata (`nil` cache means every
  check queries Postgres, which is correct but slower).
- **S3/MinIO file uploads.** The `file:upload` and `file:delete` permissions are
  seeded, but no storage backend is connected.
