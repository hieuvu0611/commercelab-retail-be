-- +goose Up
-- Development seed data for the pricing schema (scenarios SC01-SC12).
INSERT INTO pricing.sku_prices (sku_id, product_id, sku_name, unit_price_vnd, vat_rate_pct, is_published)
VALUES
    ('SKU-BALO-BLACK-20L', 'PROD-BALO-CITY', 'Balo City Đen 20L', 300000, 10, TRUE),
    ('SKU-BOTTLE-BLUE-500ML', 'PROD-BOTTLE-ECO', 'Bình Nước Xanh 500ml', 100000, 10, TRUE)
ON CONFLICT (sku_id) DO NOTHING;

-- +goose Down
DELETE FROM pricing.sku_prices WHERE sku_id IN ('SKU-BALO-BLACK-20L', 'SKU-BOTTLE-BLUE-500ML');
