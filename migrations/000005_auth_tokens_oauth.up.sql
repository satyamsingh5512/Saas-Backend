-- Refresh tokens are rotated on every use (rotation-on-use) and stored hashed.
-- family_id groups all tokens descended from one original login, so reuse of
-- an already-rotated token (a signal of theft) lets us revoke the whole
-- family in one statement instead of just the one token.
CREATE TABLE refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,
    family_id   UUID NOT NULL,
    replaced_by UUID REFERENCES refresh_tokens (id) ON DELETE SET NULL,
    user_agent  TEXT NOT NULL DEFAULT '',
    ip_address  INET,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_refresh_tokens_token_hash ON refresh_tokens (token_hash);
CREATE INDEX idx_refresh_tokens_tenant_id ON refresh_tokens (tenant_id);
CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_family_id ON refresh_tokens (family_id);
-- Cheap cleanup job target: delete expired/revoked rows in batches.
CREATE INDEX idx_refresh_tokens_expires_at ON refresh_tokens (expires_at);

ALTER TABLE refresh_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE refresh_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_refresh_tokens ON refresh_tokens
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Linked OAuth identities (Google/GitHub). A user row can have zero or more
-- linked providers in addition to (or instead of) a local password.
CREATE TABLE oauth_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider        VARCHAR(30) NOT NULL CHECK (provider IN ('google', 'github')),
    provider_user_id VARCHAR(255) NOT NULL,
    access_token    TEXT,          -- encrypted at rest by application (Phase 6 detail)
    refresh_token   TEXT,
    token_expires_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A given provider identity maps to exactly one user platform-wide.
CREATE UNIQUE INDEX uq_oauth_provider_identity ON oauth_accounts (provider, provider_user_id);
CREATE INDEX idx_oauth_accounts_tenant_id ON oauth_accounts (tenant_id);
CREATE INDEX idx_oauth_accounts_user_id ON oauth_accounts (user_id);

ALTER TABLE oauth_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_oauth_accounts ON oauth_accounts
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Generic single-use verification tokens: email verification + password reset.
-- Looked up by hash before the user is fully authenticated, same pattern as
-- invitations -- service layer resolves by hash on a privileged connection.
CREATE TABLE verification_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose     VARCHAR(30) NOT NULL CHECK (purpose IN ('email_verification', 'password_reset')),
    token_hash  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_verification_tokens_hash ON verification_tokens (token_hash);
CREATE INDEX idx_verification_tokens_user_purpose ON verification_tokens (user_id, purpose);
CREATE INDEX idx_verification_tokens_tenant_id ON verification_tokens (tenant_id);

ALTER TABLE verification_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE verification_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_verification_tokens ON verification_tokens
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
