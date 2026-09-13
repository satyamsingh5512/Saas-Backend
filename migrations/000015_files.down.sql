DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'file:view');
DELETE FROM permissions WHERE code = 'file:view';

DROP TABLE files;

-- Restore the pre-files role seed so a down/up cycle returns to the exact
-- prior behavior instead of leaving file:view references behind.
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

    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_owner_id, id, NEW.id FROM permissions;

    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_admin_id, id, NEW.id FROM permissions WHERE code <> 'org:manage';

    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_manager_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:invite', 'member:view', 'role:view',
                    'team:create', 'team:manage', 'team:view',
                    'project:create', 'project:manage', 'project:view',
                    'apikey:view', 'file:upload', 'file:delete');

    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_member_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'role:view', 'team:view',
                    'project:create', 'project:view', 'file:upload');

    INSERT INTO role_permissions (role_id, permission_id, tenant_id)
    SELECT v_guest_id, id, NEW.id FROM permissions
    WHERE code IN ('org:view', 'member:view', 'team:view', 'project:view');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

