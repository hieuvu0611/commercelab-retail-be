-- +goose Up
-- Tables for Returns, RMA dossiers and QC inspections (B06, I07, SC07, SC08)

CREATE TABLE IF NOT EXISTS support.rma_requests (
    rma_id                VARCHAR(64) PRIMARY KEY,
    order_id              VARCHAR(64) NOT NULL,
    customer_id           VARCHAR(64) NOT NULL,
    warehouse_id          VARCHAR(64) NOT NULL DEFAULT 'WH-HCM-01',
    reason                VARCHAR(32) NOT NULL, -- 'CHANGE_OF_MIND', 'SELLER_FAULT'
    status                VARCHAR(32) NOT NULL DEFAULT 'OPEN_PENDING_RECEIPT',
    -- OPEN_PENDING_RECEIPT, RECEIVED_PENDING_QC, QC_PASSED_RESTOCKED, QC_FAILED_ISOLATED, REFUND_PENDING, REFUNDED, REJECTED
    refundable_amount_vnd BIGINT NOT NULL DEFAULT 0,
    shipping_refund_vnd   BIGINT NOT NULL DEFAULT 0,
    is_exchange           BOOLEAN NOT NULL DEFAULT FALSE,
    linked_order_id       VARCHAR(64),
    notes                 TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rma_requests_order ON support.rma_requests(order_id);
CREATE INDEX IF NOT EXISTS idx_rma_requests_customer ON support.rma_requests(customer_id);
CREATE INDEX IF NOT EXISTS idx_rma_requests_status ON support.rma_requests(status);

CREATE TABLE IF NOT EXISTS support.rma_items (
    rma_id         VARCHAR(64) NOT NULL REFERENCES support.rma_requests(rma_id) ON DELETE CASCADE,
    sku_id         VARCHAR(64) NOT NULL,
    sku_name       VARCHAR(255),
    quantity       BIGINT NOT NULL CHECK (quantity > 0),
    unit_paid_vnd  BIGINT NOT NULL CHECK (unit_paid_vnd >= 0),
    qc_status      VARCHAR(32) DEFAULT 'PENDING', -- 'PENDING', 'PASS', 'FAIL'
    PRIMARY KEY (rma_id, sku_id)
);

CREATE TABLE IF NOT EXISTS support.rma_timeline (
    id         BIGSERIAL PRIMARY KEY,
    rma_id     VARCHAR(64) NOT NULL REFERENCES support.rma_requests(rma_id) ON DELETE CASCADE,
    status     VARCHAR(32) NOT NULL,
    actor      VARCHAR(64) NOT NULL,
    note       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rma_timeline_rma ON support.rma_timeline(rma_id);

-- +goose Down
DROP TABLE IF EXISTS support.rma_timeline;
DROP TABLE IF EXISTS support.rma_items;
DROP TABLE IF EXISTS support.rma_requests;
