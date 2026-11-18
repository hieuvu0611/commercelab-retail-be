-- +goose Up
ALTER TABLE support.rma_requests
    ADD COLUMN IF NOT EXISTS customer_note TEXT;

ALTER TABLE support.rma_items
    ADD COLUMN IF NOT EXISTS received_qty BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS qc_passed_qty BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS qc_failed_qty BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE support.rma_items
    DROP COLUMN IF EXISTS qc_failed_qty,
    DROP COLUMN IF EXISTS qc_passed_qty,
    DROP COLUMN IF EXISTS received_qty;

ALTER TABLE support.rma_requests
    DROP COLUMN IF EXISTS customer_note;
