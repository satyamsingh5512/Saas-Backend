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
--
-- The password is read from the :app_user_password psql variable so this file
-- never contains a real credential:
--
--   psql "$MIGRATE_DATABASE_URL" -v app_user_password="$(openssl rand -base64 24)" \
--        -f scripts/provision_app_role.sql
--
-- The Makefile target passes a local development default. Any deployment that
-- reuses that default is not isolated from anyone who has read this repository.
\if :{?app_user_password}
\else
\set app_user_password 'change-me-app-user-password'
\warn 'app_user_password was not supplied; using the insecure local default'
\endif

-- Branching happens in psql rather than in a DO block on purpose. psql does not
-- interpolate variables inside dollar-quoted strings, so `:'app_user_password'`
-- written inside a `DO $$ ... $$` body is passed through as literal text and the
-- role ends up with a password of ":'app_user_password'". Interpolating outside
-- any quoting is what makes `:'app_user_password'` expand to a correctly escaped
-- literal.
SELECT NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') AS role_missing \gset

\if :role_missing
CREATE ROLE app_user LOGIN PASSWORD :'app_user_password' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
\else
-- Re-running rotates the password rather than silently keeping the old one, so
-- this script is also the supported way to rotate the application credential.
--
-- Deliberately no NOSUPERUSER / NOBYPASSRLS on this branch. PostgreSQL restricts
-- altering either attribute to real superusers, and a managed provider's admin
-- (Aiven's avnadmin, Render's owner) is not one -- it holds CREATEROLE instead.
-- Naming them here fails with "permission denied to alter role" even when
-- setting them to the value they already hold, which would make this script
-- succeed on first run and break on every re-run. CREATE does not need them
-- either, since both attributes default to off. The verification block at the
-- end is what actually guarantees they are off.
ALTER ROLE app_user WITH LOGIN PASSWORD :'app_user_password';
\endif

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

-- EXECUTE on the SECURITY DEFINER lookup functions (migrations 000011 and
-- 000012): the three pre-auth lookups that genuinely cannot know their tenant
-- in advance -- invite token, API key, and refresh/verification token.
--
-- Those migrations do grant these, but only inside an `IF EXISTS (... rolname =
-- 'app_user')` guard. On a managed provider the schema is migrated before the
-- role exists, so the guard is false and the grants are silently skipped. The
-- flows then fail only at runtime, on login and invite acceptance. Granting here
-- unconditionally is what makes provisioning order stop mattering.
--
-- PostgreSQL also grants EXECUTE to PUBLIC by default, so these currently
-- succeed by accident. That default is exactly the kind of thing a hardening
-- pass revokes, which would turn an implicit dependency into an outage.
-- Extension-owned functions are excluded deliberately. `GRANT EXECUTE ON ALL
-- FUNCTIONS IN SCHEMA public` also targets everything pgcrypto and citext
-- installed, which the migration role does not own, producing a screenful of
-- "no privileges were granted" warnings that bury a real failure. Those already
-- carry PUBLIC EXECUTE anyway. Filtering on pg_depend deptype 'e' leaves exactly
-- this schema's own functions.
DO $$
DECLARE
    fn RECORD;
BEGIN
    FOR fn IN
        SELECT p.oid::regprocedure AS signature
          FROM pg_proc p
          JOIN pg_namespace n ON n.oid = p.pronamespace
         WHERE n.nspname = 'public'
           AND NOT EXISTS (
               SELECT 1 FROM pg_depend d
                WHERE d.objid = p.oid AND d.deptype = 'e')
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO app_user', fn.signature);
    END LOOP;
END
$$;

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO app_user;

-- Verify the two attributes the whole isolation model rests on, and refuse to
-- report success without them. A role that kept BYPASSRLS would pass every
-- application test while enforcing nothing.
DO $$
DECLARE
    v_super  BOOLEAN;
    v_bypass BOOLEAN;
BEGIN
    SELECT rolsuper, rolbypassrls INTO v_super, v_bypass
      FROM pg_roles WHERE rolname = 'app_user';

    IF v_super OR v_bypass THEN
        RAISE EXCEPTION 'app_user has rolsuper=% rolbypassrls=%; RLS would be inert', v_super, v_bypass;
    END IF;

    RAISE NOTICE 'app_user provisioned: NOSUPERUSER, NOBYPASSRLS';
END
$$;
