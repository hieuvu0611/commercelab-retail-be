-- +goose Up
-- P5-T06: what the payment captured for the order and how much of it is owed back (late or unwanted capture, SC05).
ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS captured_amount_vnd BIGINT NOT NULL DEFAULT 0 CHECK (captured_amount_vnd >= 0),
    ADD COLUMN IF NOT EXISTS refund_pending_vnd  BIGINT NOT NULL DEFAULT 0 CHECK (refund_pending_vnd >= 0);

-- +goose Down
ALTER TABLE orders.orders
    DROP COLUMN IF EXISTS refund_pending_vnd,
    DROP COLUMN IF EXISTS captured_amount_vnd;
