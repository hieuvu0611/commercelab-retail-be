-- +goose Up
-- P7-T03: how much of the captured money was already paid back (refund_pending_vnd is what is still owed).
ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS refunded_amount_vnd BIGINT NOT NULL DEFAULT 0 CHECK (refunded_amount_vnd >= 0),
    ADD CONSTRAINT chk_orders_refund_ceiling CHECK (refunded_amount_vnd + refund_pending_vnd <= captured_amount_vnd);

-- +goose Down
ALTER TABLE orders.orders
    DROP CONSTRAINT IF EXISTS chk_orders_refund_ceiling,
    DROP COLUMN IF EXISTS refunded_amount_vnd;
