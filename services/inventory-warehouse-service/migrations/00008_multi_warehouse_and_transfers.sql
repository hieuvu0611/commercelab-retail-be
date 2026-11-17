-- +goose Up
-- P12-M2-T01 & T02: Multi-warehouse stock (WH-HN-01), split-order reservations, and inter-warehouse transfers (SC09).

INSERT INTO inventory.stock_items (sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit)
VALUES
    ('SKU-BALO-BLACK-20L', 'WH-HN-01', 5, 0, 0, 0, 0),
    ('SKU-BOTTLE-BLUE-500ML', 'WH-HN-01', 10, 0, 0, 0, 0)
ON CONFLICT (sku_id, warehouse_id) DO NOTHING;

ALTER TABLE inventory.reservations DROP CONSTRAINT IF EXISTS reservations_order_id_key;
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'uq_reservations_order_warehouse'
    ) THEN
        ALTER TABLE inventory.reservations
            ADD CONSTRAINT uq_reservations_order_warehouse UNIQUE (order_id, warehouse_id);
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE inventory.stock_movements DROP CONSTRAINT IF EXISTS chk_movement_type;
ALTER TABLE inventory.stock_movements ADD CONSTRAINT chk_movement_type CHECK (type IN
    ('OPENING', 'RESERVE', 'COMMIT', 'RELEASE', 'HANDOVER', 'RETURN_RECEIVE', 'QC_PASS', 'ADJUST',
     'PO_RECEIVE_GOOD', 'PO_RECEIVE_DAMAGED', 'SUPPLIER_RETURN',
     'TRANSFER_OUT', 'TRANSFER_IN_GOOD', 'TRANSFER_IN_DAMAGED')
);

CREATE TABLE IF NOT EXISTS inventory.warehouse_transfers (
    transfer_id       VARCHAR(64) PRIMARY KEY,
    from_warehouse_id VARCHAR(32) NOT NULL,
    to_warehouse_id   VARCHAR(32) NOT NULL,
    status            VARCHAR(32) NOT NULL DEFAULT 'DRAFT',
    created_by        VARCHAR(64) NOT NULL,
    dispatched_by     VARCHAR(64) NOT NULL DEFAULT '',
    received_by       VARCHAR(64) NOT NULL DEFAULT '',
    exception_case_id VARCHAR(64) NOT NULL DEFAULT '',
    notes             TEXT NOT NULL DEFAULT '',
    dispatched_at     TIMESTAMPTZ,
    received_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_transfer_warehouses_diff CHECK (from_warehouse_id <> to_warehouse_id),
    CONSTRAINT chk_transfer_status CHECK (
        status IN ('DRAFT', 'IN_TRANSIT', 'RECEIVED', 'RECEIVED_WITH_DISCREPANCY', 'CANCELLED')
    )
);

CREATE TABLE IF NOT EXISTS inventory.warehouse_transfer_items (
    id                BIGSERIAL PRIMARY KEY,
    transfer_id       VARCHAR(64) NOT NULL REFERENCES inventory.warehouse_transfers(transfer_id) ON DELETE CASCADE,
    sku_id            VARCHAR(64) NOT NULL,
    quantity          BIGINT NOT NULL CHECK (quantity > 0),
    received_good_qty BIGINT NOT NULL DEFAULT 0 CHECK (received_good_qty >= 0),
    damaged_qty       BIGINT NOT NULL DEFAULT 0 CHECK (damaged_qty >= 0),
    missing_qty       BIGINT NOT NULL DEFAULT 0 CHECK (missing_qty >= 0),
    UNIQUE (transfer_id, sku_id)
);

CREATE INDEX IF NOT EXISTS idx_warehouse_transfers_from ON inventory.warehouse_transfers(from_warehouse_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_warehouse_transfers_to ON inventory.warehouse_transfers(to_warehouse_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS inventory.warehouse_transfer_items;
DROP TABLE IF EXISTS inventory.warehouse_transfers;
ALTER TABLE inventory.reservations DROP CONSTRAINT IF EXISTS uq_reservations_order_warehouse;
