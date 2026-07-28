-- Two more tables must be looked up by an opaque secret BEFORE any tenant
-- context exists, for the same reason as the lookups added in
-- migrations/000011 (see that file's header for the full rationale):
--
--   invitations -- accepting an invite is the one flow where the invitee may not
--   have an account at all yet, so the tenant can only be discovered FROM the
--   invite token. migrations/000004 explicitly anticipated this ("the service
--   layer fetches by token using a privileged connection") but the function it
--   depends on was never created; without it, RLS FORCE on invitations makes the
--   accept flow impossible for the low-privilege app_user role.
--
--   api_keys -- a machine client presents only its key. The tenant is an
--   attribute of the matched row, so it cannot be known before the lookup.
--
-- Both are narrowly scoped SECURITY DEFINER functions over a single indexed
-- equality match on a high-entropy SHA-256 hash. Their blast radius is
-- equivalent to the one query they perform: a caller must already possess a
-- valid secret to get any row back. No broader cross-tenant capability is
-- granted to the application role.
CREATE OR REPLACE FUNCTION find_invitation_by_hash(p_token_hash TEXT)
RETURNS SETOF invitations AS $$
    SELECT * FROM invitations WHERE token_hash = p_token_hash;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

CREATE OR REPLACE FUNCTION find_api_key_by_hash(p_key_hash TEXT)
RETURNS SETOF api_keys AS $$
    SELECT * FROM api_keys WHERE key_hash = p_key_hash;
$$ LANGUAGE sql SECURITY DEFINER SET search_path = public;

-- Accepting an invitation must create the user row, the role grant, and the
-- invitation status update as one atomic unit, but the inserts target
-- RLS-protected tables in a tenant the connection has no scope for until the
-- invite is validated. Rather than widening the app role, the accept flow calls
-- this function, which sets the tenant scope itself from the invitation row it
-- just validated and then performs all three writes transactionally.
--
-- It deliberately re-validates status and expiry inside the function instead of
-- trusting the caller: this is the security boundary, and a caller that skipped
-- those checks (or raced another accept) must not be able to redeem a revoked or
-- expired invite. The single UPDATE ... WHERE status = 'pending' is also what
-- makes concurrent redemption of the same token safe -- exactly one transaction
-- can move the row out of 'pending', and the loser aborts.
CREATE OR REPLACE FUNCTION accept_invitation(
    p_token_hash    TEXT,
    p_user_id       UUID,
    p_full_name     TEXT,
    p_password_hash TEXT
)
RETURNS UUID AS $$
DECLARE
    v_invitation invitations;
    v_user_id    UUID;
    v_updated    INT;
BEGIN
    SELECT * INTO v_invitation FROM invitations WHERE token_hash = p_token_hash;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation not found' USING ERRCODE = 'no_data_found';
    END IF;
    IF v_invitation.status <> 'pending' THEN
        RAISE EXCEPTION 'invitation is not pending' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_invitation.expires_at <= now() THEN
        RAISE EXCEPTION 'invitation has expired' USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- Claim the invitation first. If a concurrent transaction already accepted
    -- it, zero rows match and we abort before creating a duplicate user.
    UPDATE invitations
       SET status = 'accepted', accepted_at = now()
     WHERE id = v_invitation.id AND status = 'pending';

    GET DIAGNOSTICS v_updated = ROW_COUNT;
    IF v_updated = 0 THEN
        RAISE EXCEPTION 'invitation already accepted' USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- An existing user in this tenant with the invited email keeps their
    -- account; the invite then only grants the new role. This makes re-inviting
    -- an existing member a role change rather than a duplicate-account error.
    SELECT id INTO v_user_id
      FROM users
     WHERE tenant_id = v_invitation.tenant_id
       AND email = v_invitation.email
       AND deleted_at IS NULL;

    IF v_user_id IS NULL THEN
        INSERT INTO users (id, tenant_id, email, password_hash, full_name, status, email_verified_at)
        VALUES (p_user_id, v_invitation.tenant_id, v_invitation.email, p_password_hash,
                p_full_name, 'active',
                -- Redeeming an emailed invite token proves control of the
                -- mailbox, so a separate verification round trip adds nothing.
                now())
        RETURNING id INTO v_user_id;
    END IF;

    INSERT INTO user_roles (user_id, role_id, tenant_id, assigned_by)
    VALUES (v_user_id, v_invitation.role_id, v_invitation.tenant_id, v_invitation.invited_by)
    ON CONFLICT (user_id, role_id) DO NOTHING;

    RETURN v_user_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        GRANT EXECUTE ON FUNCTION find_invitation_by_hash(TEXT) TO app_user;
        GRANT EXECUTE ON FUNCTION find_api_key_by_hash(TEXT) TO app_user;
        GRANT EXECUTE ON FUNCTION accept_invitation(TEXT, UUID, TEXT, TEXT) TO app_user;
    END IF;
END
$$;
