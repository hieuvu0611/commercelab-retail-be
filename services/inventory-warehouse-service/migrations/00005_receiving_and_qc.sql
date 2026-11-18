-- +goose Up
-- Inbound receiving inspection and QC recording (B02 Stage 2, P01, SC09).
ALTER TABLE inventory.stock_movements DROP CONSTRAINT IF EXISTS chk_movement_type;
ALTER TABLE inventory.stock_movements ADD CONSTRAINT chk_movement_type CHECK (type IN
    ('OPENING', 'RESERVE', 'COMMIT', 'RELEASE', 'HANDOVER', 'RETURN_RECEIVE', 'QC_PASS', 'ADJUST',
     'PO_RECEIVE_GOOD', 'PO_RECEIVE_DAMAGED', 'SUPPLIER_RETURN')
);

CREATE TABLE IF NOT EXISTS inventory.receiving_inspections (
    inspection_id VARCHAR(64) PRIMARY KEY,
    po_id         VARCHAR(64) NOT NULL REFERENCES inventory.purchase_orders(po_id),
    warehouse_id  VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    inspector_id  VARCHAR(64) NOT NULL,
    status        VARCHAR(32) NOT NULL DEFAULT 'COMPLETED',
    notes         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS inventory.receiving_inspection_lines (
    id            BIGSERIAL PRIMARY KEY,
    inspection_id VARCHAR(64) NOT NULL REFERENCES inventory.receiving_inspections(inspection_id) ON DELETE CASCADE,
    sku_id        VARCHAR(64) NOT NULL,
    delivered_qty BIGINT NOT NULL,
    passed_qty    BIGINT NOT NULL,
    damaged_qty   BIGINT NOT NULL,
    short_qty     BIGINT NOT NULL,
    CONSTRAINT chk_inspection_line_math CHECK (
        delivered_qty = (passed_qty + damaged_qty) AND passed_qty >= 0 AND damaged_qty >= 0 AND short_qty >= 0
    )
);

CREATE INDEX IF NOT EXISTS idx_receiving_po_id ON inventory.receiving_inspections(po_id);
CREATE INDEX IF NOT EXISTS idx_receiving_warehouse ON inventory.receiving_inspections(warehouse_id);

-- +goose Down
DROP TABLE IF EXISTS inventory.receiving_inspection_lines;
DROP TABLE IF EXISTS inventory.receiving_inspections;
ALTER TABLE inventory.stock_movements DROP CONSTRAINT IF EXISTS chk_movement_type;
ALTER TABLE inventory.stock_movements ADD CONSTRAINT chk_movement_type CHECK (type IN
    ('OPENING', 'RESERVE', 'COMMIT', 'RELEASE', 'HANDOVER', 'RETURN_RECEIVE', 'QC_PASS', 'ADJUST')
);
