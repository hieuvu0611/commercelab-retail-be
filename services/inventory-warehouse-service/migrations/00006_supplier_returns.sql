-- +goose Up
-- Supplier returns (RTV) for unsellable / damaged goods (B02 Stage 2, I01, I06).
CREATE TABLE IF NOT EXISTS inventory.supplier_returns (
    return_id       VARCHAR(64) PRIMARY KEY,
    supplier_id     VARCHAR(64) NOT NULL REFERENCES inventory.suppliers(supplier_id),
    warehouse_id    VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    po_id           VARCHAR(64) NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL DEFAULT 'DRAFT',
    created_by      VARCHAR(64) NOT NULL,
    dispatched_by   VARCHAR(64) NOT NULL DEFAULT '',
    dispatched_at   TIMESTAMPTZ,
    tracking_number VARCHAR(64) NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_supplier_return_status CHECK (
        status IN ('DRAFT', 'DISPATCHED', 'COMPLETED', 'CANCELLED')
    )
);

CREATE TABLE IF NOT EXISTS inventory.supplier_return_lines (
    id          BIGSERIAL PRIMARY KEY,
    return_id   VARCHAR(64) NOT NULL REFERENCES inventory.supplier_returns(return_id) ON DELETE CASCADE,
    sku_id      VARCHAR(64) NOT NULL,
    quantity    BIGINT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    CONSTRAINT chk_supplier_return_qty CHECK (quantity > 0)
);

CREATE INDEX IF NOT EXISTS idx_supplier_returns_supplier ON inventory.supplier_returns(supplier_id);
CREATE INDEX IF NOT EXISTS idx_supplier_returns_warehouse ON inventory.supplier_returns(warehouse_id);

-- +goose Down
DROP TABLE IF EXISTS inventory.supplier_return_lines;
DROP TABLE IF EXISTS inventory.supplier_returns;
