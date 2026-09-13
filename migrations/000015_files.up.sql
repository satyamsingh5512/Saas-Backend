-- Tenant-scoped file metadata. Bytes are stored externally through the
-- configured ObjectStore; this table is the authorization and quota ledger.
CREATE TABLE files (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    project_id    UUID REFERENCES projects (id) ON DELETE SET NULL,
    uploaded_by   UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    original_name VARCHAR(255) NOT NULL,
    storage_key   TEXT NOT NULL,
    content_type  VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream',
    size_bytes    BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256        CHAR(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_files_storage_key ON files (storage_key);
CREATE INDEX idx_files_tenant_created ON files (tenant_id, created_at DESC);
CREATE INDEX idx_files_project_created ON files (project_id, created_at DESC);

ALTER TABLE files ENABLE ROW LEVEL SECURITY;
ALTER TABLE files FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_files ON files
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Add a read capability without renumbering or deleting existing permission
-- codes. The backfill covers tenants that already existed before this migration.
INSERT INTO permissions (code, resource, action, description)
VALUES ('file:view', 'file', 'view', 'View and download files')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id, tenant_id)
SELECT r.id, p.id, r.tenant_id
FROM roles r
CROSS JOIN permissions p
WHERE p.code = 'file:view'
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- The seed trigger in 000008 lists each role's default permissions explicitly,
-- so new tenants would otherwise miss file:view even though existing tenants
-- are backfilled above. Redefine it here with the corrected lists rather than
-- adding a second trigger that grants the same permission to every role.
CREATE OR REPLACE FUNCTION seed_default_roles() RETURNS TRIGGER AS $$
DECLARE
    v_owner_id   UUID;
    v_admin_id   UUID;
    v_manager_id UUID;
    v_member_id  UUID;
    v_guest_id   UUID;
BEGIN
    INSERT INTO roles (tenant_id, name, slug, description, is_system, rank)
    VALUES (NEW.id, 'Owner', 'owner', 'Full control including billing and org deletion', true, 0)
    RETURNING id INTO v_owner_id;

    INSERT INTO roles (tenant_id, name, slug, description, is_system, rank)
    VALUES (NEW.id, 'Admin', 'admin', 'Manage members, roles, and org settings', true, 10)
    RETURNING id INTO v_admin_id;

    INSERT INTO roles (tenant_id, name, slug, description, is_system, rank)
    VALUES (NEW.id, 'Manager', 'manager', 'Manage teams and projects', true, 20)
    RETURNING id INTO v_manager_id;

    INSERT INTO roles (tenant_id, name, slug, description, is_system, rank)
    VALUES (NEW.id, 'Member', 'member', 'Standard contributor access', true, 30)
    RETURNING id INTO v_member_id;

    INSERT INTO roles (tenant_id, name, slug, description, is_system, rank)
    VALUES (NEW.id, 'Guest', 'guest', 'Read-only access to shared resources', true, 40)
    RETURNING id INTO v_guest_id;

    -- Owner: every permission in the catalog (now includes file:view).
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_owner_id, id, NEW.id FROM permissions;

    -- Admin: everything except org:manage (now includes file:view).
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_admin_id, id, NEW.id FROM permissions WHERE code <> 'org:manage';

    -- Manager: team/project/member management, no billing or role management.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_manager_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:invite', 'member:view', 'role:view',
                    'team:create', 'team:manage', 'team:view',
                    'project:create', 'project:manage', 'project:view',
                    'apikey:view', 'file:view', 'file:upload', 'file:delete');

    -- Member: standard contributor, read + create content, no management.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_member_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'role:view', 'team:view',
                    'project:create', 'project:view', 'file:view', 'file:upload');

    -- Guest: read-only.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_guest_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'team:view', 'project:view', 'file:view');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

