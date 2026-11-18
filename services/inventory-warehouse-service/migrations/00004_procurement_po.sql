-- +goose Up
-- Procurement suppliers, purchase orders and lines (B02 Stage 2, P01, P10, SC09).
CREATE TABLE IF NOT EXISTS inventory.suppliers (
    supplier_id     VARCHAR(64) PRIMARY KEY,
    name            VARCHAR(255) NOT NULL,
    code            VARCHAR(64) NOT NULL UNIQUE,
    contact_email   VARCHAR(255) NOT NULL DEFAULT '',
    phone           VARCHAR(64) NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS inventory.purchase_orders (
    po_id            VARCHAR(64) PRIMARY KEY,
    supplier_id      VARCHAR(64) NOT NULL REFERENCES inventory.suppliers(supplier_id),
    warehouse_id     VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    status           VARCHAR(32) NOT NULL DEFAULT 'DRAFT',
    total_amount_vnd BIGINT NOT NULL DEFAULT 0,
    created_by       VARCHAR(64) NOT NULL,
    approved_by      VARCHAR(64) NOT NULL DEFAULT '',
    approved_at      TIMESTAMPTZ,
    notes            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_po_status CHECK (
        status IN ('DRAFT', 'PENDING_APPROVAL', 'APPROVED', 'REJECTED', 'ORDERED', 'PARTIALLY_RECEIVED', 'COMPLETED', 'CANCELLED')
    ),
    CONSTRAINT chk_po_total_non_negative CHECK (total_amount_vnd >= 0)
);

CREATE TABLE IF NOT EXISTS inventory.purchase_order_lines (
    id             BIGSERIAL PRIMARY KEY,
    po_id          VARCHAR(64) NOT NULL REFERENCES inventory.purchase_orders(po_id) ON DELETE CASCADE,
    sku_id         VARCHAR(64) NOT NULL,
    ordered_qty    BIGINT NOT NULL,
    unit_cost_vnd  BIGINT NOT NULL,
    received_qty   BIGINT NOT NULL DEFAULT 0,
    damaged_qty    BIGINT NOT NULL DEFAULT 0,
    short_qty      BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_po_line_quantities CHECK (
        ordered_qty > 0 AND unit_cost_vnd >= 0 AND received_qty >= 0 AND damaged_qty >= 0 AND short_qty >= 0
    )
);

CREATE INDEX IF NOT EXISTS idx_po_supplier ON inventory.purchase_orders(supplier_id);
CREATE INDEX IF NOT EXISTS idx_po_warehouse_status ON inventory.purchase_orders(warehouse_id, status);
CREATE INDEX IF NOT EXISTS idx_po_lines_po_id ON inventory.purchase_order_lines(po_id);

-- Seed initial test suppliers
INSERT INTO inventory.suppliers (supplier_id, name, code, contact_email, phone, address, active)
VALUES 
    ('SUP-EVEREST-01', 'Everest Outdoor Gear Co.', 'EVEREST', 'supply@everestgear.vn', '0901234567', 'Lot 4, Tan Binh Industrial Park, HCMC', true),
    ('SUP-HYDRO-01', 'Hydro Flask Vietnam Ltd.', 'HYDRO', 'sales@hydroflask.vn', '0912345678', 'District 7, HCMC', true)
ON CONFLICT (supplier_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS inventory.purchase_order_lines;
DROP TABLE IF EXISTS inventory.purchase_orders;
DROP TABLE IF EXISTS inventory.suppliers;
