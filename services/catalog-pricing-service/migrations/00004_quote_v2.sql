-- +goose Up
-- P4-T05: a quote is built from the customer's cart for a delivery address; `version_hash` fingerprints its
-- content so a client can tell that prices, voucher or totals changed.
ALTER TABLE pricing.quotes
    ADD COLUMN IF NOT EXISTS address_id   VARCHAR(64),
    ADD COLUMN IF NOT EXISTS discount_pct INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS version_hash VARCHAR(16) NOT NULL DEFAULT '';

-- Quotes saved before this migration derived the percentage from the amounts.
UPDATE pricing.quotes SET discount_pct = discount_vnd * 100 / subtotal_vnd WHERE subtotal_vnd > 0;

-- +goose Down
ALTER TABLE pricing.quotes
    DROP COLUMN IF EXISTS version_hash,
    DROP COLUMN IF EXISTS discount_pct,
    DROP COLUMN IF EXISTS address_id;
