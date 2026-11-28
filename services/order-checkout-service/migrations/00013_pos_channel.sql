-- +goose Up
ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS channel VARCHAR(16) NOT NULL DEFAULT 'ONLINE' CHECK (channel IN ('ONLINE', 'POS')),
    ADD COLUMN IF NOT EXISTS receipt_number VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cash_received_vnd BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS change_due_vnd BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS customer_note TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_orders_channel_wh_created
    ON orders.orders (channel, warehouse_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS orders.idx_orders_channel_wh_created;
ALTER TABLE orders.orders
    DROP COLUMN IF EXISTS customer_note,
    DROP COLUMN IF EXISTS change_due_vnd,
    DROP COLUMN IF EXISTS cash_received_vnd,
    DROP COLUMN IF EXISTS receipt_number,
    DROP COLUMN IF EXISTS channel;
