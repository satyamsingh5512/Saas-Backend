# Tenant SaaS Backend

Multi-tenant SaaS API backend built with Go, Gin, GORM, and PostgreSQL.

This README is written as a system design document with GitHub-rendered diagrams
(Mermaid), so architecture is visible directly on the repository page.

## 1) What This Project Does

`Tenant-Saas-Backend` provides a secure backend for SaaS applications where many
tenants (organizations) share one application instance and one database.

Core capabilities:

- Tenant onboarding: create tenant + first admin user.
- Authentication: login and receive JWT token.
- Authorization: protect APIs and enforce role-based access.
- Tenant isolation: every protected query is scoped to the tenant in JWT claims.
- Health and operational readiness endpoints for local/dev deployment.

## 2) High-Level Architecture

```mermaid
flowchart LR
      C[Client App or API Consumer] --> R[HTTP Router\ninternal/routes]
      R --> MW[Middleware Layer\nJWT Auth + Rate Limit]
      MW --> H[Handlers\nAuth / Users / Health]
      H --> A[Auth Utils\nJWT + Password Hashing]
      H --> M[Models\nTenant, User]
      H --> DB[(PostgreSQL)]
      CFG[Config\ninternal/config] --> R
      CFG --> H
      DBX[DB Bootstrap\ninternal/db] --> DB
```

## 3) Request Lifecycle (How It Works)

```mermaid
sequenceDiagram
      participant U as User/Client
      participant API as Gin API
      participant MW as Auth Middleware
      participant H as Handler
      participant PG as PostgreSQL

      U->>API: POST /api/v1/auth/login (email, password)
      API->>H: Route to AuthHandler.Login
      H->>PG: Lookup user by email
      PG-->>H: User + tenant_id + password_hash
      H->>H: Verify bcrypt password
      H->>H: Issue JWT (user_id, tenant_id, role)
      H-->>U: token

      U->>API: GET /api/v1/users + Bearer token
      API->>MW: Validate JWT + extract claims
      MW-->>API: Context contains tenant_id/user_id/role
      API->>H: Route to UserHandler.List
      H->>PG: SELECT users WHERE tenant_id = claims.tenant_id
      PG-->>H: Tenant-scoped user list
      H-->>U: 200 OK (only same-tenant users)
```

## 4) Tenant Isolation Model

```mermaid
flowchart TD
      T[Incoming Protected Request] --> J[JWT Validation]
      J --> C{Token valid?}
      C -- No --> X[401 Unauthorized]
      C -- Yes --> CL[Extract tenant_id from claims]
      CL --> Q[Run DB query with WHERE tenant_id = claims.tenant_id]
      Q --> R[Return tenant-scoped response]
```

Design rule:

- The backend never trusts tenant identifiers from request body/query/path for
   authorization decisions.
- Tenant context is derived from signed JWT claims only.

## 5) Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.26 |
| HTTP Framework | Gin |
| ORM | GORM + PostgreSQL driver |
| Database | PostgreSQL |
| AuthN/AuthZ | JWT (HS256), role checks |
| Password Security | bcrypt hashing |
| Local Infra | Docker Compose |
| Tooling | Makefile (`build`, `run`, `test`, `fmt`, `vet`) |

## 6) Project Structure

```text
cmd/server/main.go               Application entrypoint
internal/config/config.go        Environment-based configuration
internal/db/db.go                DB connection and initialization
internal/models/tenant.go        Tenant model
internal/models/user.go          User model
internal/auth/jwt.go             JWT generation and validation
internal/auth/password.go        Password hash/verify helpers
internal/middleware/auth.go      JWT/role middleware
internal/middleware/ratelimit.go Rate limiter for auth endpoints
internal/handlers/               HTTP handlers (auth, users, health)
internal/routes/routes.go        Route registration
```

## 7) API Surface

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/health` | No | Liveness check |
| POST | `/api/v1/auth/register` | No | Create tenant + first admin |
| POST | `/api/v1/auth/login` | No | Authenticate and return JWT |
| GET | `/api/v1/me` | Yes | Return caller identity |
| GET | `/api/v1/users` | Yes | List users for caller tenant |

### Example: Register

```json
{
   "tenant_name": "Acme Inc",
   "tenant_slug": "acme",
   "email": "admin@acme.com",
   "password": "supersecret123"
}
```

### Example: Login

```json
{
   "email": "admin@acme.com",
   "password": "supersecret123"
}
```

Use token as:

```http
Authorization: Bearer <token>
```

## 8) Local Setup

```bash
cp .env.example .env
make db-up
make run
```

Server default: `http://localhost:8080`.

## 9) Development Commands

```bash
make build
make test
make test-verbose
make vet
make fmt
make db-up
make db-down
```

## 10) Security Notes

- Set a strong `JWT_SECRET` before non-local use.
- Auth endpoints are rate-limited (`20 req/min`, burst `5`) to reduce brute force risk.
- Current rate limiter is in-memory per instance; for multi-instance deployments,
   move to shared storage (for example Redis-backed rate limiting).


## 11) Web Workspace

The server includes a responsive, dark-mode workspace dashboard at `/`. Its
light-purple visual system is embedded in the Go binary, so there is no separate
frontend build, static-host deployment, or CORS configuration to maintain.

The dashboard is wired to the existing API:

- Create a tenant and first administrator with the **Create workspace** flow.
- Sign in with the JWT-backed **Sign in** flow.
- View the caller's scoped identity, workspace health, access role, and
  tenant-isolated user directory.
- JWTs are stored in browser `sessionStorage` and clear when the tab/session
  closes. For a cross-device persistent session, implement secure HttpOnly
  cookie authentication at the API layer.

## 12) Production Container Deployment

The included `Dockerfile` produces a minimal, non-root image containing both the
Gin API and embedded workspace dashboard. Build and run it against your managed
PostgreSQL instance by supplying the normal configuration environment variables:

```bash
docker build -t tenant-saas:latest .
docker run --rm -p 8080:8080 \
  -e APP_ENV=production \
  -e DB_HOST=your-postgres-host \
  -e DB_PASSWORD='use-a-secret-manager' \
  -e DB_SSLMODE=require \
  -e JWT_SECRET='use-a-long-random-secret' \
  tenant-saas:latest
```

For an all-container deployment (appropriate for a private VM or non-production
preview), set strong `DB_PASSWORD` and `JWT_SECRET` values in your deployment
environment and run:

```bash
docker compose -f docker-compose.production.yml up -d --build
```

Do not expose the PostgreSQL service publicly. Put the application behind a TLS
terminating reverse proxy or managed load balancer, set `DB_SSLMODE=require` for
remote databases, and keep `JWT_SECRET` in your platform's secret manager.


## 13) Render Deployment

`render.yaml` defines a web service and a same-region Render Postgres database.
It wires the database's private `connectionString` into `DATABASE_URL` and asks
Render to generate a persistent `JWT_SECRET`; neither secret is committed to the
repository. `DATABASE_URL` takes precedence over the individual `DB_*` settings.

For a new deployment, create a **Blueprint** from the repository and review the
service/database names, region, and paid plans in `render.yaml` before applying
it. The service health check is `GET /health`.

To fix an existing Render web service without recreating it:

1. Create a Render Postgres instance in the same region as the web service.
2. In the web service's **Environment** settings, set `DATABASE_URL` to the
   database's **Internal Database URL** (not `localhost` and not the external URL).
3. Set `JWT_SECRET` to a new random value of at least 32 characters. Do not reuse
   the sample value in `.env.example`.
4. Set `APP_ENV=production`, remove any stale `DB_HOST`, `DB_PORT`, `DB_USER`,
   `DB_PASSWORD`, and `DB_NAME` overrides, then deploy the revision containing
   `DATABASE_URL` support.

The server now refuses to start in production with a missing/short JWT secret or
an unconfigured database, producing an actionable configuration error instead of
silently attempting `localhost:5432`.
