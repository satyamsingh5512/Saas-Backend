-- The app role is provisioned outside the migration chain. Reverting this
-- migration removes only the privileges introduced here; it does not drop or
-- mutate the role itself.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        EXECUTE 'GRANT UPDATE, DELETE ON audit_logs TO app_user';
        EXECUTE 'REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public FROM app_user';
        EXECUTE 'REVOKE USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public FROM app_user';
    END IF;
END
$$;
