-- +goose Up
-- P12-T01: COD payment method support and risk controls
ALTER TABLE orders.orders
    ADD COLUMN IF NOT EXISTS payment_method VARCHAR(16) NOT NULL DEFAULT 'PREPAID';

CREATE INDEX IF NOT EXISTS idx_orders_user_cod_active
ON orders.orders (user_id, status, payment_method, payment_status);

-- +goose Down
DROP INDEX IF EXISTS orders.idx_orders_user_cod_active;
ALTER TABLE orders.orders DROP COLUMN IF EXISTS payment_method;
