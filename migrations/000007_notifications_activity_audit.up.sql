-- Notifications: in-app, per-user. Populated asynchronously by Kafka
-- consumers reacting to domain events (invitation accepted, role changed, etc.)
CREATE TABLE notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    type        VARCHAR(50) NOT NULL,      -- e.g. 'invitation_accepted', 'role_changed'
    title       VARCHAR(255) NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_tenant_id ON notifications (tenant_id);
-- Primary access pattern: "my unread notifications, newest first".
CREATE INDEX idx_notifications_user_unread ON notifications (user_id, created_at DESC) WHERE read_at IS NULL;

ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_notifications ON notifications
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Activity feed: human-readable, org/project-scoped stream of "what happened"
-- built from the same domain events as notifications, but fanned out
-- differently (feed = per-resource timeline, notification = per-user alert).
CREATE TABLE activity_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    actor_id    UUID REFERENCES users (id) ON DELETE SET NULL,
    -- Polymorphic target (project, team, org...) identified by type+id rather
    -- than a FK, since the feed spans multiple resource types.
    target_type VARCHAR(50) NOT NULL,
    target_id   UUID NOT NULL,
    verb        VARCHAR(50) NOT NULL,      -- e.g. 'created', 'updated', 'archived'
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_events_tenant_id ON activity_events (tenant_id);
-- Primary access pattern: "activity for this project/team, newest first".
CREATE INDEX idx_activity_events_target ON activity_events (target_type, target_id, created_at DESC);

ALTER TABLE activity_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_activity_events ON activity_events
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Audit log: immutable, security/administrative action trail. Distinct from
-- activity_events (which is product-facing and can include mundane actions);
-- audit_logs is compliance-facing and covers auth, permission, billing,
-- and data-access-relevant actions specifically. No UPDATE/DELETE grants are
-- issued to the application role for this table (append-only, enforced at
-- the DB role/grant level, configured operationally, not in this migration).
CREATE TABLE audit_logs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    actor_id    UUID REFERENCES users (id) ON DELETE SET NULL,
    action      VARCHAR(100) NOT NULL,     -- e.g. 'user.role_changed', 'auth.password_reset'
    target_type VARCHAR(50),
    target_id   UUID,
    ip_address  INET,
    user_agent  TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_tenant_id ON audit_logs (tenant_id);
CREATE INDEX idx_audit_logs_tenant_created ON audit_logs (tenant_id, created_at DESC);
CREATE INDEX idx_audit_logs_actor_id ON audit_logs (actor_id);

ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_audit_logs ON audit_logs
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- User preferences: 1:1 with users, split out rather than columns-on-users so
-- frequent preference updates don't bloat the users row's update churn/MVCC
-- bloat on a table that's read on every authenticated request.
CREATE TABLE user_preferences (
    user_id                 UUID PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    tenant_id               UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    timezone                VARCHAR(50) NOT NULL DEFAULT 'UTC',
    locale                  VARCHAR(10) NOT NULL DEFAULT 'en-US',
    theme                   VARCHAR(20) NOT NULL DEFAULT 'system'
                                CHECK (theme IN ('system', 'light', 'dark')),
    email_notifications     BOOLEAN NOT NULL DEFAULT true,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_preferences_tenant_id ON user_preferences (tenant_id);

ALTER TABLE user_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_user_preferences ON user_preferences
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
