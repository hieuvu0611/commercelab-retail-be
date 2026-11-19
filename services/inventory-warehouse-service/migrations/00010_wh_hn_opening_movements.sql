-- +goose Up
-- Backfill OPENING movements for WH-HN-01 opening stock seeded in 00008 using its literal seed quantities
-- so databases with prior WH-HN-01 stock movements do not double-count movements (I06).
INSERT INTO inventory.stock_movements (sku_id, warehouse_id, type, qty, ref_type, actor_id, reason)
SELECT v.sku_id, v.warehouse_id, 'OPENING', v.opening_qty, 'SEED', 'system:migration', 'WH-HN-01 opening stock seeded in migration 00008'
FROM (VALUES
    ('SKU-BALO-BLACK-20L', 'WH-HN-01', 5::bigint),
    ('SKU-BOTTLE-BLUE-500ML', 'WH-HN-01', 10::bigint)
) AS v(sku_id, warehouse_id, opening_qty)
JOIN inventory.stock_items s
  ON s.sku_id = v.sku_id AND s.warehouse_id = v.warehouse_id
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM inventory.stock_movements
WHERE warehouse_id = 'WH-HN-01' AND type = 'OPENING' AND actor_id = 'system:migration';
