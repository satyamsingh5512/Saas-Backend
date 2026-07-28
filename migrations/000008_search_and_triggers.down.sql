DROP TRIGGER IF EXISTS trg_subscriptions_updated_at ON subscriptions;
DROP TRIGGER IF EXISTS trg_oauth_accounts_updated_at ON oauth_accounts;
DROP TRIGGER IF EXISTS trg_projects_updated_at ON projects;
DROP TRIGGER IF EXISTS trg_teams_updated_at ON teams;
DROP TRIGGER IF EXISTS trg_roles_updated_at ON roles;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP TRIGGER IF EXISTS trg_tenants_updated_at ON tenants;
DROP FUNCTION IF EXISTS set_updated_at();

DROP TRIGGER IF EXISTS trg_seed_default_roles ON tenants;
DROP FUNCTION IF EXISTS seed_default_roles();

DROP INDEX IF EXISTS idx_projects_search_vector;
ALTER TABLE projects DROP COLUMN IF EXISTS search_vector;
