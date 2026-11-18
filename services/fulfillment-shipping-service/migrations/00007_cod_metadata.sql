-- +goose Up
-- P12-T03: COD metadata and collection amounts on shipments
ALTER TABLE fulfillment.shipments
    ADD COLUMN IF NOT EXISTS payment_method VARCHAR(16) NOT NULL DEFAULT 'PREPAID',
    ADD COLUMN IF NOT EXISTS cod_amount_to_collect BIGINT NOT NULL DEFAULT 0 CHECK (cod_amount_to_collect >= 0);

-- +goose Down
ALTER TABLE fulfillment.shipments
    DROP COLUMN IF EXISTS cod_amount_to_collect,
    DROP COLUMN IF EXISTS payment_method;
