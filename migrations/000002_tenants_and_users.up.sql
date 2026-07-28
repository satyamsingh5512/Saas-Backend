-- Tenants (organizations). Root of the multi-tenancy model. Never itself
-- tenant-scoped -- this table has no tenant_id and no RLS policy.
CREATE TABLE tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    slug            CITEXT NOT NULL,
    plan_code       VARCHAR(50) NOT NULL DEFAULT 'free',
    status          VARCHAR(20) NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'suspended', 'pending_deletion')),
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

-- Slug is the public-facing subdomain identifier used for tenant resolution
-- pre-auth (see architecture Phase 2 / Phase 7). Must be unique among live tenants;
-- a partial unique index lets a deleted tenant's slug be reused later.
CREATE UNIQUE INDEX uq_tenants_slug_live ON tenants (slug) WHERE deleted_at IS NULL;
CREATE INDEX idx_tenants_deleted_at ON tenants (deleted_at) WHERE deleted_at IS NOT NULL;

COMMENT ON TABLE tenants IS 'Root tenant/organization record. Not itself tenant-scoped.';

-- Users are tenant-scoped: a person with accounts in two orgs gets two rows,
-- one per tenant, rather than a single global identity. This matches the
-- shared-schema/shared-database isolation model chosen in the architecture
-- design and keeps "delete my account in this org" operationally simple.
CREATE TABLE users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    email               CITEXT NOT NULL,
    password_hash       TEXT,                    -- NULL for OAuth-only accounts
    full_name           VARCHAR(255) NOT NULL DEFAULT '',
    avatar_url          TEXT,
    status              VARCHAR(20) NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'invited', 'disabled')),
    email_verified_at   TIMESTAMPTZ,
    last_login_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

-- Composite uniqueness: same email may exist across different tenants (a person
-- can be a member of multiple orgs with the same email) but not twice within
-- one tenant. Partial index excludes soft-deleted rows so an offboarded user's
-- email can be reused/re-invited.
CREATE UNIQUE INDEX uq_users_tenant_email_live ON users (tenant_id, email) WHERE deleted_at IS NULL;
-- Every tenant-scoped query filters by tenant_id first; this index makes that
-- filter (and the FK join from every other tenant-scoped table) cheap.
CREATE INDEX idx_users_tenant_id ON users (tenant_id);
CREATE INDEX idx_users_deleted_at ON users (deleted_at) WHERE deleted_at IS NOT NULL;

COMMENT ON TABLE users IS 'Tenant-scoped user identity. RLS-protected.';

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;

-- Standard tenant-isolation policy applied to every tenant-scoped table:
-- the current transaction must have set app.tenant_id (see pkg/db.WithTenantTx)
-- for any row to be visible or writable. current_setting(..., true) returns ''
-- rather than erroring when unset, which makes an unscoped connection see zero
-- rows (fail closed) instead of erroring or leaking data.
CREATE POLICY tenant_isolation_users ON users
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
