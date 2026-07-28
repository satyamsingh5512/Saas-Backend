-- FORCE ROW LEVEL SECURITY (applied on every tenant-scoped table in prior
-- migrations) makes RLS apply even to the table owner, which is the primary
-- defense. As a second layer, we also expect the application to connect as
-- a non-superuser role (see docs/deployment.md and terraform/rds.tf for
-- provisioning). BYPASSRLS is never granted to the application role.
--
-- We do not create the role here via CREATE ROLE because migration tooling
-- typically runs with different (often more privileged, sometimes
-- differently-scoped) credentials than the application, and managed Postgres
-- providers (RDS, Cloud SQL, Render) frequently restrict CREATE ROLE for
-- non-admin migration users. Role provisioning is handled by
-- terraform/modules/database (Phase "Terraform IaC") or, for local dev, by
-- docker-compose's POSTGRES_USER matching the app's DB_USER directly.
--
-- This migration documents the requirement in SQL comments and adds a guard
-- so CI/tests can assert RLS is actually enforced end-to-end (see
-- internal/tenancy repository tests) regardless of which role runs them.
COMMENT ON SCHEMA public IS
    'Application must connect as a non-superuser role WITHOUT the BYPASSRLS attribute for tenant isolation guarantees to hold.';
