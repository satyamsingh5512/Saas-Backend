-- When the restricted app_user role is provisioned before migrations (the
-- production Compose path), its default privileges cover newly-created tables
-- but the append-only audit restriction still needs to be applied after the
-- audit_logs table exists. This migration is a no-op on deployments that use a
-- provider-managed runtime role instead of app_user.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA public TO app_user';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_user';
        EXECUTE 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_user';
        EXECUTE 'REVOKE UPDATE, DELETE ON audit_logs FROM app_user';
    END IF;
END
$$;
