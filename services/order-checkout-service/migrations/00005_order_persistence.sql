-- +goose Up
-- P5-T01: order persistence for the checkout saga (B03, I02).
-- An idempotency key belongs to one customer: two customers may use the same key without colliding.
ALTER TABLE orders.idempotency_keys DROP CONSTRAINT IF EXISTS idempotency_keys_pkey;
ALTER TABLE orders.idempotency_keys ADD PRIMARY KEY (user_id, idempotency_key);
CREATE UNIQUE INDEX IF NOT EXISTS idx_idempotency_keys_order ON orders.idempotency_keys (order_id);

ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS address_id   VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS warehouse_id VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS failure_code VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expires_at   TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_orders_user_created ON orders.orders (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders.orders (status, created_at);

ALTER TABLE orders.order_items
    ADD CONSTRAINT fk_order_items_order FOREIGN KEY (order_id) REFERENCES orders.orders (order_id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_order_items_order ON orders.order_items (order_id);

-- +goose Down
DROP INDEX IF EXISTS orders.idx_order_items_order;
ALTER TABLE orders.order_items DROP CONSTRAINT IF EXISTS fk_order_items_order;
DROP INDEX IF EXISTS orders.idx_orders_status;
DROP INDEX IF EXISTS orders.idx_orders_user_created;
ALTER TABLE orders.orders
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS warehouse_id,
    DROP COLUMN IF EXISTS address_id;
DROP INDEX IF EXISTS orders.idx_idempotency_keys_order;
ALTER TABLE orders.idempotency_keys DROP CONSTRAINT IF EXISTS idempotency_keys_pkey;
ALTER TABLE orders.idempotency_keys ADD PRIMARY KEY (idempotency_key);
