-- +goose Up
-- Backfill OPENING movements for WH-HN-01 opening stock seeded in 00008 so the movement ledger explains on_hand (I06).
INSERT INTO inventory.stock_movements (sku_id, warehouse_id, type, qty, ref_type, actor_id, reason)
SELECT sku_id, warehouse_id, 'OPENING', on_hand, 'SEED', 'system:migration', 'WH-HN-01 opening stock seeded in migration 00008'
FROM inventory.stock_items
WHERE warehouse_id = 'WH-HN-01' AND on_hand > 0
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM inventory.stock_movements
WHERE warehouse_id = 'WH-HN-01' AND type = 'OPENING' AND actor_id = 'system:migration';
