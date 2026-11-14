-- +goose Up
-- Development seed data for the orders schema (scenarios SC01-SC12).
INSERT INTO orders.voucher_campaigns (voucher_code, discount_pct, total_quota, reserved_quota, consumed_quota)
VALUES
    ('WELCOME10', 10, 100, 0, 0)
ON CONFLICT (voucher_code) DO NOTHING;

-- +goose Down
DELETE FROM orders.voucher_campaigns WHERE voucher_code = 'WELCOME10';
