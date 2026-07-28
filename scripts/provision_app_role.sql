-- Provisions the low-privilege application database role described in
-- migration 000009's schema comment: a NOSUPERUSER, NOBYPASSRLS role that
-- the application connects as in production, so that RLS policies are
-- actually enforced against it (Postgres superusers and BYPASSRLS roles
-- ignore RLS entirely, including FORCE ROW LEVEL SECURITY).
--
-- This script is intentionally NOT part of the golang-migrate up/down chain
-- in migrations/. Managed Postgres providers (RDS, Cloud SQL, Render) often
-- restrict CREATE ROLE / GRANT for the credential used to run schema
-- migrations, so baking role provisioning into the main migration chain
-- would break `migrate up` on those providers. Instead:
--
--   - Local/dev/CI/Testcontainers: run this script once after `make migrate-up`
--     (see Makefile target `db-provision-app-role`), using the superuser
--     credential that IS permitted to create roles locally.
--   - Managed/production: provision the equivalent role via
--     terraform/modules/database (Phase "Terraform IaC"), which uses the
--     cloud provider's native user/role management instead of raw SQL.
--
-- Safe to re-run: every statement is idempotent.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        CREATE ROLE app_user LOGIN PASSWORD 'change-me-app-user-password' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO app_user', current_database());
END
$$;

GRANT USAGE ON SCHEMA public TO app_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_user;

-- audit_logs is append-only from the application's perspective: no UPDATE or
-- DELETE grant, so even a fully compromised application credential cannot
-- tamper with or erase the audit trail (see migration 000007's design comment).
REVOKE UPDATE, DELETE ON audit_logs FROM app_user;

-- Ensure tables created by future migrations (run as the superuser/owner
-- role) automatically grant the same privileges to app_user without a manual
-- GRANT statement in every future migration file.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO app_user;
