-- Platform-wide aggregate counts for /metrics and operator tooling.
--
-- RLS on every tenant-scoped table is FORCE-enabled against app_user, and the
-- fail-closed policy hides all rows when app.tenant_id is unset -- which is
-- exactly right for request traffic, but makes "how many users exist
-- platform-wide" unanswerable from the application credential by design.
--
-- The sanctioned escape hatch is the same one migrations/000011 and 000012
-- established: a narrowly scoped SECURITY DEFINER function owned by the
-- migration (superuser) role. It returns ONLY aggregate counts -- no tenant
-- PII, no row contents -- so its blast radius is a handful of integers.
-- EXECUTE is granted to app_user; no table-level grant is widened.
CREATE OR REPLACE FUNCTION platform_stats()
RETURNS TABLE (
    tenants             BIGINT,
    users               BIGINT,
    teams               BIGINT,
    projects            BIGINT,
    pending_invitations BIGINT,
    active_api_keys     BIGINT,
    files               BIGINT,
    storage_bytes       BIGINT,
    audit_events_30d    BIGINT,
    active_users_7d     BIGINT
) AS $$
    SELECT
        (SELECT COUNT(*) FROM tenants WHERE deleted_at IS NULL),
        (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL),
        (SELECT COUNT(*) FROM teams WHERE deleted_at IS NULL),
        (SELECT COUNT(*) FROM projects WHERE deleted_at IS NULL),
        (SELECT COUNT(*) FROM invitations WHERE status = 'pending' AND expires_at > NOW()),
        (SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())),
        (SELECT COUNT(*) FROM files),
        (SELECT COALESCE(SUM(size_bytes), 0) FROM files),
        (SELECT COUNT(*) FROM audit_logs WHERE created_at > NOW() - INTERVAL '30 days'),
        (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND last_login_at > NOW() - INTERVAL '7 days');
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION platform_stats() TO app_user';
    END IF;
END
$$;
