-- +goose Up
-- Development vouchers (idempotent): WELCOME10 for SC01, LASTONE10 with a single use for SC10.
INSERT INTO orders.voucher_campaigns (voucher_code, name, discount_pct, total_quota, per_customer_limit, status)
VALUES
    ('WELCOME10', 'Welcome 10% off', 10, 100, 100, 'ACTIVE'),
    ('LASTONE10', 'Last single use 10% off (SC10)', 10, 1, 1, 'ACTIVE')
ON CONFLICT (voucher_code) DO UPDATE SET
    name = EXCLUDED.name,
    per_customer_limit = EXCLUDED.per_customer_limit,
    status = EXCLUDED.status;

-- +goose Down
DELETE FROM orders.voucher_campaigns WHERE voucher_code = 'LASTONE10';
UPDATE orders.voucher_campaigns SET name = '', per_customer_limit = 1 WHERE voucher_code = 'WELCOME10';
