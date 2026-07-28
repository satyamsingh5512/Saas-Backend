-- Dynamic RBAC. Roles are per-tenant rows (not a hardcoded enum) so a tenant's
-- admin can rename/clone roles and adjust permission sets without a code
-- deploy. Five system roles (Owner/Admin/Manager/Member/Guest) are seeded per
-- tenant at creation time and marked is_system=true so they cannot be deleted,
-- but their permission sets can still be edited -- this is the "dynamic
-- permissions" requirement from the spec.
CREATE TABLE roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    slug        VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_system   BOOLEAN NOT NULL DEFAULT false,
    -- Lower rank = more powerful. Used to prevent privilege escalation (a
    -- Manager cannot grant a role with a lower rank number than their own).
    rank        SMALLINT NOT NULL DEFAULT 100,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_roles_tenant_slug ON roles (tenant_id, slug);
CREATE INDEX idx_roles_tenant_id ON roles (tenant_id);

ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE roles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_roles ON roles
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Permissions are a GLOBAL catalog (platform-defined capability strings, e.g.
-- "project:create", "billing:manage"), not tenant-scoped -- tenants compose
-- roles out of this fixed catalog rather than inventing new capability
-- strings, which keeps authorization checks in code (permission constants)
-- stable while still letting role->permission mapping be fully dynamic per
-- tenant. This is deliberately NOT RLS-protected; it's platform metadata.
CREATE TABLE permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        VARCHAR(150) NOT NULL,      -- e.g. 'project:create'
    resource    VARCHAR(100) NOT NULL,      -- e.g. 'project'
    action      VARCHAR(50) NOT NULL,       -- e.g. 'create'
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_permissions_code ON permissions (code);
CREATE INDEX idx_permissions_resource ON permissions (resource);

-- Join table: which permissions a given role grants. Tenant-scoped via the
-- owning role, RLS-protected the same way.
CREATE TABLE role_permissions (
    role_id       UUID NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions (id) ON DELETE CASCADE,
    tenant_id     UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id)
);

CREATE INDEX idx_role_permissions_tenant_id ON role_permissions (tenant_id);
CREATE INDEX idx_role_permissions_permission_id ON role_permissions (permission_id);

ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permissions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_role_permissions ON role_permissions
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Join table: which roles a user holds within their (single, by row design)
-- tenant. A user can hold multiple roles simultaneously (permissions union).
CREATE TABLE user_roles (
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id     UUID NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    assigned_by UUID REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_user_roles_tenant_id ON user_roles (tenant_id);
CREATE INDEX idx_user_roles_role_id ON user_roles (role_id);

ALTER TABLE user_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_roles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_user_roles ON user_roles
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Seed the global permission catalog. Additive-only in future migrations --
-- never renumber/delete a code once shipped, since role_permissions rows and
-- application code reference these by `code` string constants.
INSERT INTO permissions (code, resource, action, description) VALUES
    ('org:manage',        'org',         'manage', 'Update organization settings, billing, and delete the organization'),
    ('org:view',          'org',         'view',   'View organization settings'),
    ('member:invite',     'member',      'invite', 'Invite new members to the organization'),
    ('member:remove',     'member',      'remove', 'Remove members from the organization'),
    ('member:view',       'member',      'view',   'View organization member list'),
    ('role:manage',       'role',        'manage', 'Create, edit, and assign roles and permissions'),
    ('role:view',         'role',        'view',   'View roles and permissions'),
    ('team:create',       'team',        'create', 'Create teams'),
    ('team:manage',       'team',        'manage', 'Update or delete teams, manage team membership'),
    ('team:view',         'team',        'view',   'View teams'),
    ('project:create',    'project',     'create', 'Create projects'),
    ('project:manage',    'project',     'manage', 'Update, archive, or delete projects'),
    ('project:view',      'project',     'view',   'View projects'),
    ('project:delete',    'project',     'delete', 'Delete projects'),
    ('apikey:manage',     'apikey',      'manage', 'Create and revoke API keys'),
    ('apikey:view',       'apikey',      'view',   'View API keys'),
    ('billing:manage',    'billing',     'manage', 'Manage subscription plan and payment details'),
    ('billing:view',      'billing',     'view',   'View billing and subscription information'),
    ('file:upload',       'file',        'upload', 'Upload files/attachments'),
    ('file:delete',       'file',        'delete', 'Delete files/attachments'),
    ('audit:view',        'audit',       'view',   'View audit logs');
