-- Postgres full-text search support for projects (search requirement, Phase 1).
-- Generated column avoids recomputing the tsvector application-side and keeps
-- the GIN index consistent automatically on every INSERT/UPDATE.
ALTER TABLE projects ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(description, '')), 'B')
    ) STORED;

CREATE INDEX idx_projects_search_vector ON projects USING GIN (search_vector);

-- Function + triggers to auto-provision the five system roles (Owner, Admin,
-- Manager, Member, Guest) with a sensible default permission set whenever a
-- new tenant is created. Roles remain editable afterwards (dynamic RBAC
-- requirement) -- this only seeds sane defaults, it doesn't hardcode
-- enforcement.
-- SECURITY DEFINER: this function must be able to insert into roles/
-- role_permissions for a brand-new tenant BEFORE the inserting connection
-- has (or could have) set app.tenant_id for that tenant -- the tenant row
-- is still mid-INSERT when this trigger fires, so there is no prior
-- opportunity to set the session variable, and app_user's own RLS policies
-- would otherwise reject every row this function tries to insert. Running
-- as SECURITY DEFINER (owned by the migration superuser) makes the
-- function itself bypass RLS for its own writes while every other access
-- to these tables by app_user remains fully RLS-enforced. This is a
-- narrowly-scoped, deliberate exception: only role-seeding on tenant
-- creation runs elevated, nothing else does.
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

    -- Owner: every permission in the catalog.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_owner_id, id, NEW.id FROM permissions;

    -- Admin: everything except org:manage (org deletion/ownership transfer).
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_admin_id, id, NEW.id FROM permissions WHERE code <> 'org:manage';

    -- Manager: team/project/member management, no billing or role management.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_manager_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:invite', 'member:view', 'role:view',
                    'team:create', 'team:manage', 'team:view',
                    'project:create', 'project:manage', 'project:view',
                    'apikey:view', 'file:upload', 'file:delete');

    -- Member: standard contributor, read + create content, no management.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_member_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'role:view', 'team:view',
                    'project:create', 'project:view', 'file:upload');

    -- Guest: read-only.
    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_guest_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'team:view', 'project:view');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

CREATE TRIGGER trg_seed_default_roles
    AFTER INSERT ON tenants
    FOR EACH ROW
    EXECUTE FUNCTION seed_default_roles();

-- updated_at maintenance trigger, applied to every table with an updated_at
-- column, so application code never has to remember to set it manually.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tenants_updated_at BEFORE UPDATE ON tenants FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_roles_updated_at BEFORE UPDATE ON roles FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_teams_updated_at BEFORE UPDATE ON teams FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_projects_updated_at BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_oauth_accounts_updated_at BEFORE UPDATE ON oauth_accounts FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_subscriptions_updated_at BEFORE UPDATE ON subscriptions FOR EACH ROW EXECUTE FUNCTION set_updated_at();
