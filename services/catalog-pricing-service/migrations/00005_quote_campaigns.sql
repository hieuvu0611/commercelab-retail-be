-- +goose Up
-- B08: Store applied promotion campaign discounts alongside quotes.
ALTER TABLE pricing.quotes
    ADD COLUMN IF NOT EXISTS applied_campaigns_json JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE pricing.quotes
    DROP COLUMN IF EXISTS applied_campaigns_json;
