-- +goose Up
-- Baseline schema for fulfillment (fulfillment-shipping-service — B04, B05, P06, SC06), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS fulfillment.shipments (
    shipment_id      VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL UNIQUE,
    warehouse_id     VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    status           VARCHAR(32) NOT NULL, -- PENDING_PICK, PICKING, PACKING, STOPPED_FOR_CANCEL, HANDED_OVER, DELIVERED, DELIVERY_FAILED, RETURNING
    version          BIGINT NOT NULL DEFAULT 1,
    handed_over_at   TIMESTAMPTZ,
    delivered_at     TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS fulfillment.shipments;
