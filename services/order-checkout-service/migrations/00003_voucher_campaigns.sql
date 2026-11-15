-- +goose Up
-- P4-T03: voucher campaigns become the single source of truth for discount rules (window, per-customer limit, status).
ALTER TABLE orders.voucher_campaigns
    ADD COLUMN IF NOT EXISTS name               VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS starts_at          TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS ends_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS per_customer_limit INT NOT NULL DEFAULT 1 CHECK (per_customer_limit >= 1),
    ADD COLUMN IF NOT EXISTS status             VARCHAR(16) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'PAUSED')),
    ADD COLUMN IF NOT EXISTS created_by         VARCHAR(64) NOT NULL DEFAULT 'system',
    ADD COLUMN IF NOT EXISTS created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE orders.voucher_campaigns
    ADD CONSTRAINT chk_voucher_window CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at);

-- One row per order that holds or used a voucher (written by the checkout saga, P12); read here to apply the
-- per-customer limit. RESERVED and CONSUMED count against the limit, RELEASED does not.
CREATE TABLE IF NOT EXISTS orders.voucher_usages (
    id           BIGSERIAL PRIMARY KEY,
    voucher_code VARCHAR(64) NOT NULL REFERENCES orders.voucher_campaigns (voucher_code),
    user_id      VARCHAR(64) NOT NULL,
    order_id     VARCHAR(64) NOT NULL,
    state        VARCHAR(16) NOT NULL CHECK (state IN ('RESERVED', 'CONSUMED', 'RELEASED')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (voucher_code, order_id)
);

CREATE INDEX IF NOT EXISTS idx_voucher_usages_customer ON orders.voucher_usages (voucher_code, user_id);

-- +goose Down
DROP TABLE IF EXISTS orders.voucher_usages;
ALTER TABLE orders.voucher_campaigns DROP CONSTRAINT IF EXISTS chk_voucher_window;
ALTER TABLE orders.voucher_campaigns
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS per_customer_limit,
    DROP COLUMN IF EXISTS ends_at,
    DROP COLUMN IF EXISTS starts_at,
    DROP COLUMN IF EXISTS name;
