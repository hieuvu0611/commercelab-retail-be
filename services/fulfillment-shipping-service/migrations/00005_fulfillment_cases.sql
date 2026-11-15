-- +goose Up
-- P6-T05: exception cases for Support/Operations (B04): pick shortage, failed delivery, partial delivery.
-- An open PICK_SHORTAGE case also means the SKU is blocked in that warehouse until the shelf is investigated.
CREATE TABLE IF NOT EXISTS fulfillment.cases (
    case_id      VARCHAR(160) PRIMARY KEY,
    case_type    VARCHAR(32) NOT NULL,
    status       VARCHAR(16) NOT NULL DEFAULT 'OPEN',
    shipment_id  VARCHAR(64) NOT NULL REFERENCES fulfillment.shipments (shipment_id) ON DELETE CASCADE,
    order_id     VARCHAR(64) NOT NULL,
    warehouse_id VARCHAR(32) NOT NULL,
    sku_id       VARCHAR(64) NOT NULL DEFAULT '',
    quantity     BIGINT NOT NULL DEFAULT 0,
    reason       VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cases_warehouse_status ON fulfillment.cases (warehouse_id, status, created_at);

-- +goose Down
DROP INDEX IF EXISTS fulfillment.idx_cases_warehouse_status;
DROP TABLE IF EXISTS fulfillment.cases;
