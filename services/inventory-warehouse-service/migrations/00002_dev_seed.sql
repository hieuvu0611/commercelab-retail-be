-- +goose Up
-- Development seed data for the inventory schema (scenarios SC01-SC12).
INSERT INTO inventory.stock_items (sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit)
VALUES
    ('SKU-BALO-BLACK-20L', 'WH-HCM-01', 10, 0, 0, 0, 0),
    ('SKU-BOTTLE-BLUE-500ML', 'WH-HCM-01', 20, 0, 0, 0, 0)
ON CONFLICT (sku_id, warehouse_id) DO NOTHING;

-- +goose Down
DELETE FROM inventory.stock_items WHERE sku_id IN ('SKU-BALO-BLACK-20L', 'SKU-BOTTLE-BLUE-500ML') AND warehouse_id = 'WH-HCM-01';
