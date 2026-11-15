-- +goose Up
-- Stock movement ledger (I06): who changed which stock bucket, why and when. One row per SKU line of every change.
CREATE TABLE IF NOT EXISTS inventory.stock_movements (
    id           BIGSERIAL PRIMARY KEY,
    sku_id       VARCHAR(64)  NOT NULL,
    warehouse_id VARCHAR(32)  NOT NULL,
    type         VARCHAR(24)  NOT NULL,
    qty          BIGINT       NOT NULL,
    ref_type     VARCHAR(32)  NOT NULL DEFAULT '',
    ref_id       VARCHAR(64)  NOT NULL DEFAULT '',
    actor_id     VARCHAR(64)  NOT NULL,
    reason       TEXT         NOT NULL DEFAULT '',
    occurred_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_movement_type CHECK (type IN
        ('OPENING', 'RESERVE', 'COMMIT', 'RELEASE', 'HANDOVER', 'RETURN_RECEIVE', 'QC_PASS', 'ADJUST')),
    CONSTRAINT chk_movement_qty CHECK (qty <> 0 AND (type = 'ADJUST' OR qty > 0))
);

CREATE INDEX IF NOT EXISTS idx_stock_movements_lookup
    ON inventory.stock_movements (warehouse_id, sku_id, occurred_at DESC, id DESC);

-- Opening stock is recorded once per SKU and warehouse; later increases come from receiving (Phase 9).
CREATE UNIQUE INDEX IF NOT EXISTS uq_stock_movements_opening
    ON inventory.stock_movements (sku_id, warehouse_id) WHERE type = 'OPENING';

-- Stock that existed before the ledger (dev seed) gets its opening record so the ledger explains on hand.
INSERT INTO inventory.stock_movements (sku_id, warehouse_id, type, qty, actor_id, reason)
SELECT sku_id, warehouse_id, 'OPENING', on_hand, 'system:migration', 'stock present before the ledger existed'
FROM inventory.stock_items
WHERE on_hand > 0
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS inventory.stock_movements;
