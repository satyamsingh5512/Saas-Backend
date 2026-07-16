# Tenant SaaS Backend

Multi-tenant SaaS API backend built with Go, Gin, GORM, and PostgreSQL.

## Stack

- **Language:** Go 1.26
- **Framework:** Gin
- **ORM:** GORM (PostgreSQL driver)
- **Auth:** JWT (HS256) + bcrypt password hashing
- **Multi-tenancy:** shared database, `tenant_id` column scoping enforced via JWT claims (no client-supplied tenant IDs are trusted)

## Project structure

```
cmd/server/main.go          entrypoint
internal/config/            env config loading
internal/db/                DB connection + migrations
internal/models/            GORM models (Tenant, User)
internal/auth/               JWT + password hashing helpers
internal/middleware/        auth middleware (JWT validation, role checks)
internal/handlers/          HTTP handlers (auth, users, health)
internal/routes/            route registration
```

## Setup

1. Copy `.env.example` to `.env` and fill in values (especially `JWT_SECRET`):
   ```
   cp .env.example .env
   ```

2. Start PostgreSQL locally (via Docker Compose):
   ```
   make db-up
   ```

3. Run the server:
   ```
   make run
   ```

The server starts on `http://localhost:8080` (configurable via `PORT`).

## Development

```
make build          # build binary to ./bin/server
make test           # run unit + integration tests
make test-verbose   # run tests with verbose output
make vet            # go vet
make fmt            # gofmt
make db-up/db-down  # manage local Postgres container
```

Integration tests in `internal/handlers` require a reachable Postgres instance
(configured the same way as the app, via `.env`/environment variables) and will
automatically skip if the database is unavailable.

## API

| Method | Path                  | Auth | Description                          |
|--------|-----------------------|------|---------------------------------------|
| GET    | /health               | no   | Liveness check                        |
| POST   | /api/v1/auth/register | no   | Create a tenant + first admin user    |
| POST   | /api/v1/auth/login    | no   | Log in, returns JWT                   |
| GET    | /api/v1/me            | yes  | Current user's identity               |
| GET    | /api/v1/users         | yes  | List users in the caller's tenant     |

### Register

```
POST /api/v1/auth/register
{
  "tenant_name": "Acme Inc",
  "tenant_slug": "acme",
  "email": "admin@acme.com",
  "password": "supersecret123"
}
```

### Login

```
POST /api/v1/auth/login
{
  "email": "admin@acme.com",
  "password": "supersecret123"
}
```

Use the returned `token` as `Authorization: Bearer <token>` on protected routes.

## Notes / security

- `JWT_SECRET` must be set to a long random value before running outside local dev. The app logs a warning if it's empty.
- Tenant scoping is derived exclusively from the validated JWT, never from request bodies/params, to prevent cross-tenant data access.
- `/api/v1/auth/*` endpoints are rate-limited per-IP (20 req/min, burst 5) via an in-memory token bucket (`internal/middleware/ratelimit.go`) to slow down brute force/credential stuffing. This is in-memory and per-instance; for multi-instance deployments behind a load balancer, replace with a shared store (e.g. Redis-backed limiter).
