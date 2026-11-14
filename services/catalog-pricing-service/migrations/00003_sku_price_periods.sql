-- +goose Up
-- P2-T06: prices with effective periods. sku_price_history holds every period of every SKU (including
-- scheduled ones) and is the source of truth for the price in effect; the new sku_prices columns mirror the
-- latest period that has started.
ALTER TABLE pricing.sku_prices
    ADD COLUMN IF NOT EXISTS effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS effective_to   TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS pricing.sku_price_history (
    id              BIGSERIAL    PRIMARY KEY,
    sku_id          VARCHAR(64)  NOT NULL,
    unit_price_vnd  BIGINT       NOT NULL CHECK (unit_price_vnd >= 0),
    effective_from  TIMESTAMPTZ  NOT NULL,
    effective_to    TIMESTAMPTZ,
    changed_by      VARCHAR(64)  NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT sku_price_history_period CHECK (effective_to IS NULL OR effective_to > effective_from)
);
CREATE INDEX IF NOT EXISTS idx_sku_price_history_sku ON pricing.sku_price_history (sku_id, effective_from DESC);

-- Existing prices become the first open period of their SKU.
INSERT INTO pricing.sku_price_history (sku_id, unit_price_vnd, effective_from, changed_by)
SELECT sku_id, unit_price_vnd, LEAST(updated_at, NOW()), 'migration'
FROM pricing.sku_prices p
WHERE NOT EXISTS (SELECT 1 FROM pricing.sku_price_history h WHERE h.sku_id = p.sku_id);

-- +goose Down
DROP TABLE IF EXISTS pricing.sku_price_history;
ALTER TABLE pricing.sku_prices DROP COLUMN IF EXISTS effective_to, DROP COLUMN IF EXISTS effective_from;
