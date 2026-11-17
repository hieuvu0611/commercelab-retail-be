-- +goose Up
-- B08: Full Promotion Campaigns with budget caps, eligibility rules, priority, stacking, and atomic usage tracking.
CREATE TABLE IF NOT EXISTS orders.promotion_campaigns (
    campaign_id           VARCHAR(64) PRIMARY KEY,
    code                  VARCHAR(64) NOT NULL UNIQUE,
    name                  VARCHAR(255) NOT NULL,
    discount_type         VARCHAR(16) NOT NULL CHECK (discount_type IN ('PERCENT', 'FIXED_AMOUNT')),
    discount_value        BIGINT NOT NULL CHECK (discount_value > 0),
    max_discount_vnd      BIGINT NOT NULL DEFAULT 0 CHECK (max_discount_vnd >= 0),
    min_order_vnd         BIGINT NOT NULL DEFAULT 0 CHECK (min_order_vnd >= 0),
    eligible_category_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    eligible_sku_ids      JSONB NOT NULL DEFAULT '[]'::jsonb,
    customer_segment      VARCHAR(32) NOT NULL DEFAULT 'ALL' CHECK (customer_segment IN ('ALL', 'NEW_CUSTOMER', 'VIP')),
    auto_apply            BOOLEAN NOT NULL DEFAULT FALSE,
    stackable             BOOLEAN NOT NULL DEFAULT FALSE,
    priority              INT NOT NULL DEFAULT 100,
    total_quota           BIGINT NOT NULL CHECK (total_quota > 0),
    reserved_quota        BIGINT NOT NULL DEFAULT 0 CHECK (reserved_quota >= 0),
    consumed_quota        BIGINT NOT NULL DEFAULT 0 CHECK (consumed_quota >= 0),
    budget_vnd            BIGINT NOT NULL DEFAULT 0 CHECK (budget_vnd >= 0),
    budget_reserved_vnd   BIGINT NOT NULL DEFAULT 0 CHECK (budget_reserved_vnd >= 0),
    budget_consumed_vnd   BIGINT NOT NULL DEFAULT 0 CHECK (budget_consumed_vnd >= 0),
    per_customer_limit    INT NOT NULL DEFAULT 1 CHECK (per_customer_limit >= 1),
    starts_at             TIMESTAMPTZ,
    ends_at               TIMESTAMPTZ,
    status                VARCHAR(16) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'PAUSED')),
    version               BIGINT NOT NULL DEFAULT 1,
    created_by            VARCHAR(64) NOT NULL DEFAULT 'system',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_promo_quota_buckets CHECK (reserved_quota + consumed_quota <= total_quota),
    CONSTRAINT chk_promo_budget_buckets CHECK (budget_vnd = 0 OR budget_reserved_vnd + budget_consumed_vnd <= budget_vnd),
    CONSTRAINT chk_promo_window CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at)
);

CREATE TABLE IF NOT EXISTS orders.promotion_usages (
    id           BIGSERIAL PRIMARY KEY,
    campaign_id  VARCHAR(64) NOT NULL REFERENCES orders.promotion_campaigns (campaign_id),
    user_id      VARCHAR(64) NOT NULL,
    order_id     VARCHAR(64) NOT NULL,
    discount_vnd BIGINT NOT NULL DEFAULT 0 CHECK (discount_vnd >= 0),
    state        VARCHAR(16) NOT NULL CHECK (state IN ('RESERVED', 'CONSUMED', 'RELEASED')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (campaign_id, order_id)
);

CREATE INDEX IF NOT EXISTS idx_promotion_usages_customer ON orders.promotion_usages (campaign_id, user_id);
CREATE INDEX IF NOT EXISTS idx_promotion_usages_order ON orders.promotion_usages (order_id);

ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS applied_promotions_json JSONB NOT NULL DEFAULT '[]'::jsonb;

INSERT INTO orders.promotion_campaigns (
    campaign_id, code, name, discount_type, discount_value, max_discount_vnd, min_order_vnd,
    eligible_category_ids, eligible_sku_ids, customer_segment, auto_apply, stackable, priority,
    total_quota, budget_vnd, per_customer_limit, status, created_by
) VALUES
(
    'CAMP-TIER1M',
    'TIER1M5',
    'Auto 5% Off Orders >= 1,000,000 VND',
    'PERCENT',
    5,
    100000,
    1000000,
    '[]'::jsonb,
    '[]'::jsonb,
    'ALL',
    TRUE,
    TRUE,
    50,
    1000,
    100000000,
    5,
    'ACTIVE',
    'seed'
),
(
    'CAMP-GEAR15',
    'GEAR15',
    'Outdoor & Travel Gear 15% Off (Max 150k)',
    'PERCENT',
    15,
    150000,
    300000,
    '[]'::jsonb,
    '["SKU-BALO-BLACK-20L","SKU-BOTTLE-BLUE-500ML"]'::jsonb,
    'ALL',
    FALSE,
    FALSE,
    10,
    500,
    50000000,
    2,
    'ACTIVE',
    'seed'
)
ON CONFLICT (campaign_id) DO NOTHING;

-- +goose Down
ALTER TABLE orders.orders DROP COLUMN IF EXISTS applied_promotions_json;
DROP TABLE IF EXISTS orders.promotion_usages;
DROP TABLE IF EXISTS orders.promotion_campaigns;
