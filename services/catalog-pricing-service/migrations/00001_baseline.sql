-- +goose Up
-- Baseline schema for pricing (catalog-pricing-service — B01, P02, P08, I04), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS pricing.sku_prices (
    sku_id          VARCHAR(64) PRIMARY KEY,
    product_id      VARCHAR(64) NOT NULL,
    sku_name        VARCHAR(255) NOT NULL,
    unit_price_vnd  BIGINT NOT NULL CHECK (unit_price_vnd >= 0), -- Price includes 10% VAT
    vat_rate_pct    INT NOT NULL DEFAULT 10,
    is_published    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS pricing.quotes (
    quote_id            VARCHAR(64) PRIMARY KEY,
    user_id             VARCHAR(64) NOT NULL,
    voucher_code        VARCHAR(64),
    subtotal_vnd        BIGINT NOT NULL CHECK (subtotal_vnd >= 0),
    discount_vnd        BIGINT NOT NULL CHECK (discount_vnd >= 0),
    shipping_fee_vnd    BIGINT NOT NULL CHECK (shipping_fee_vnd >= 0),
    total_payable_vnd   BIGINT NOT NULL CHECK (total_payable_vnd >= 0),
    lines_json          JSONB NOT NULL DEFAULT '[]'::jsonb,
    version             INT NOT NULL DEFAULT 1,
    expires_at          TIMESTAMPTZ NOT NULL, -- P02: a quote is valid for 5 minutes
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS pricing.quotes;
DROP TABLE IF EXISTS pricing.sku_prices;
