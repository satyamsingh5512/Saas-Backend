-- Subscription plans are a GLOBAL catalog (platform-defined), not tenant-scoped,
-- same rationale as `permissions`: tenants subscribe TO a plan, they don't
-- define their own.
CREATE TABLE subscription_plans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            VARCHAR(50) NOT NULL,
    name            VARCHAR(100) NOT NULL,
    price_cents     INTEGER NOT NULL DEFAULT 0,
    billing_period  VARCHAR(20) NOT NULL DEFAULT 'monthly'
                        CHECK (billing_period IN ('monthly', 'yearly')),
    max_seats       INTEGER,             -- NULL = unlimited
    max_projects    INTEGER,
    max_storage_mb  INTEGER,
    features        JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_subscription_plans_code ON subscription_plans (code);

CREATE TABLE subscriptions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    plan_id               UUID NOT NULL REFERENCES subscription_plans (id) ON DELETE RESTRICT,
    status                VARCHAR(20) NOT NULL DEFAULT 'active'
                              CHECK (status IN ('trialing', 'active', 'past_due', 'canceled')),
    -- Opaque identifier from the external payment provider (e.g. Stripe
    -- subscription ID). NULL for internally-managed free plans.
    provider_subscription_id VARCHAR(255),
    current_period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end    TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '30 days'),
    cancel_at_period_end  BOOLEAN NOT NULL DEFAULT false,
    canceled_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active subscription per tenant at a time.
CREATE UNIQUE INDEX uq_subscriptions_tenant_id ON subscriptions (tenant_id);
CREATE INDEX idx_subscriptions_plan_id ON subscriptions (plan_id);
CREATE INDEX idx_subscriptions_status ON subscriptions (status);

ALTER TABLE subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_subscriptions ON subscriptions
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

INSERT INTO subscription_plans (code, name, price_cents, billing_period, max_seats, max_projects, max_storage_mb, features) VALUES
    ('free',       'Free',       0,     'monthly', 5,    3,    500,   '{"api_access": false, "sso": false}'::jsonb),
    ('pro',        'Pro',       2900,   'monthly', 50,   50,   10240, '{"api_access": true, "sso": false}'::jsonb),
    ('enterprise', 'Enterprise', 0,     'monthly', NULL, NULL, NULL,  '{"api_access": true, "sso": true}'::jsonb);
