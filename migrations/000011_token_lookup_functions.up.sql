-- Three tables intentionally need to be looked up by an opaque, high-entropy
-- secret (a hashed token) BEFORE the caller has any tenant context:
-- refresh_tokens (token refresh), verification_tokens (password reset /
-- email verification), and oauth_accounts (linking an OAuth identity back
-- to a user). Because RLS on these tables is FORCE-enabled, a low-privilege
-- app_user connection cannot read a row without app.tenant_id already set
-- -- but the whole point of these lookups is to discover the tenant from
-- the token, not the other way around.
--
-- The fix is NOT to weaken RLS or add a bypass role to the application's
-- credential (that would defeat the isolation guarantee for every other
-- query the app makes). Instead, each lookup is exposed through a narrowly
-- scoped SECURITY DEFINER function that returns only the single row
-- matching the given hash -- equivalent in blast radius to "SELECT ... WHERE
-- token_hash = $1", just running with the elevated privilege needed to see
-- across tenants for that one indexed equality lookup only. No other
-- capability is granted.
CREATE OR REPLACE FUNCTION find_refresh_token_by_hash(p_token_hash TEXT)
RETURNS SETOF refresh_tokens AS $$
    SELECT * FROM refresh_tokens WHERE token_hash = p_token_hash;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

CREATE OR REPLACE FUNCTION find_verification_token_by_hash(p_token_hash TEXT)
RETURNS SETOF verification_tokens AS $$
    SELECT * FROM verification_tokens WHERE token_hash = p_token_hash;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

CREATE OR REPLACE FUNCTION find_oauth_account_by_provider(p_provider TEXT, p_provider_user_id TEXT)
RETURNS SETOF oauth_accounts AS $$
    SELECT * FROM oauth_accounts WHERE provider = p_provider AND provider_user_id = p_provider_user_id;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

-- Login without a pre-known tenant (email/password only, no subdomain or
-- tenant_slug supplied) has the same problem: the app must find every user
-- row matching an email across ALL tenants before it can even attempt a
-- password check, since the tenant is only known once the correct account
-- is identified. This scans the users table by an equality-indexed column
-- only, no data beyond email match is exposed to callers who don't already
-- know a valid (email, password) pair for the returned row.
CREATE OR REPLACE FUNCTION find_users_by_email(p_email TEXT)
RETURNS SETOF users AS $$
    SELECT * FROM users WHERE email = p_email AND deleted_at IS NULL;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

-- Grant EXECUTE (not SELECT on the underlying tables) to app_user if it
-- already exists in this environment. In managed/production deployments
-- where app_user is provisioned separately via Terraform (see
-- migrations/000009's schema comment), grant it there instead using the
-- equivalent statement -- see docs/deployment.md.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        GRANT EXECUTE ON FUNCTION find_refresh_token_by_hash(TEXT) TO app_user;
        GRANT EXECUTE ON FUNCTION find_verification_token_by_hash(TEXT) TO app_user;
        GRANT EXECUTE ON FUNCTION find_oauth_account_by_provider(TEXT, TEXT) TO app_user;
        GRANT EXECUTE ON FUNCTION find_users_by_email(TEXT) TO app_user;
    END IF;
END
$$;
