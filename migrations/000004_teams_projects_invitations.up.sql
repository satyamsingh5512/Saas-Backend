CREATE TABLE teams (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    slug        CITEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by  UUID REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_teams_tenant_slug_live ON teams (tenant_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX idx_teams_tenant_id ON teams (tenant_id);

ALTER TABLE teams ENABLE ROW LEVEL SECURITY;
ALTER TABLE teams FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_teams ON teams
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

CREATE TABLE team_members (
    team_id     UUID NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX idx_team_members_tenant_id ON team_members (tenant_id);
-- Supports "which teams is this user in" lookups (reverse of the PK order).
CREATE INDEX idx_team_members_user_id ON team_members (user_id);

ALTER TABLE team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_team_members ON team_members
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

CREATE TABLE projects (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    team_id     UUID REFERENCES teams (id) ON DELETE SET NULL,
    name        VARCHAR(255) NOT NULL,
    slug        CITEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      VARCHAR(20) NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'archived')),
    created_by  UUID REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_projects_tenant_slug_live ON projects (tenant_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX idx_projects_tenant_id ON projects (tenant_id);
-- Common list view: "active projects for team X" ordered by recency.
CREATE INDEX idx_projects_team_status ON projects (team_id, status) WHERE deleted_at IS NULL;

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_projects ON projects
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

CREATE TABLE project_members (
    project_id  UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX idx_project_members_tenant_id ON project_members (tenant_id);
CREATE INDEX idx_project_members_user_id ON project_members (user_id);

ALTER TABLE project_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_project_members ON project_members
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Invitations are looked up by an unguessable token BEFORE the invitee is
-- authenticated (they may not even have an account yet), so this table
-- cannot rely on the RLS session variable for its primary lookup path --
-- the service layer fetches by token using a privileged connection, then
-- validates tenant/expiry/status in application code. It is still tagged
-- with tenant_id and RLS-protected for all OTHER access patterns (e.g. an
-- admin listing pending invitations for their org).
CREATE TABLE invitations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    email        CITEXT NOT NULL,
    role_id      UUID NOT NULL REFERENCES roles (id) ON DELETE RESTRICT,
    token_hash   TEXT NOT NULL,           -- SHA-256 of the opaque invite token
    status       VARCHAR(20) NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    invited_by   UUID REFERENCES users (id) ON DELETE SET NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_invitations_token_hash ON invitations (token_hash);
CREATE INDEX idx_invitations_tenant_id ON invitations (tenant_id);
-- Enforce at most one live pending invite per (tenant, email) to avoid token
-- spam / duplicate invites; accepted/revoked/expired rows are excluded so a
-- new invite can be sent after the previous one resolves.
CREATE UNIQUE INDEX uq_invitations_tenant_email_pending ON invitations (tenant_id, email) WHERE status = 'pending';

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_invitations ON invitations
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- API keys for machine-to-machine access. Only the hash is stored; the
-- plaintext key is shown once at creation time, same pattern as GitHub/Stripe.
CREATE TABLE api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id      UUID REFERENCES users (id) ON DELETE SET NULL,
    name         VARCHAR(255) NOT NULL,
    key_prefix   VARCHAR(12) NOT NULL,     -- shown in UI for identification, e.g. "sk_live_ab"
    key_hash     TEXT NOT NULL,            -- SHA-256 of the full key
    scopes       TEXT[] NOT NULL DEFAULT '{}',  -- subset of permission codes
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_api_keys_key_hash ON api_keys (key_hash);
CREATE INDEX idx_api_keys_tenant_id ON api_keys (tenant_id);
CREATE INDEX idx_api_keys_user_id ON api_keys (user_id);

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_api_keys ON api_keys
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
