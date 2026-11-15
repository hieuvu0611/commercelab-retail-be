-- +goose Up
-- P6-T01: shipments created from OrderConfirmed (B04). The warehouse always comes from the order, never a default.
ALTER TABLE fulfillment.shipments ALTER COLUMN warehouse_id DROP DEFAULT;
ALTER TABLE fulfillment.shipments
    ADD COLUMN IF NOT EXISTS user_id    VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE INDEX IF NOT EXISTS idx_shipments_warehouse_status ON fulfillment.shipments (warehouse_id, status, created_at);

CREATE TABLE IF NOT EXISTS fulfillment.shipment_items (
    shipment_id VARCHAR(64) NOT NULL REFERENCES fulfillment.shipments (shipment_id) ON DELETE CASCADE,
    sku_id      VARCHAR(64) NOT NULL,
    quantity    BIGINT NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (shipment_id, sku_id)
);

-- Consumer dedupe: an event id is recorded in the transaction of the change it caused.
CREATE TABLE IF NOT EXISTS fulfillment.processed_events (
    event_id     VARCHAR(64) PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS fulfillment.outbox_events (
    id             BIGSERIAL PRIMARY KEY,
    event_id       VARCHAR(64) NOT NULL UNIQUE,
    event_type     VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(32) NOT NULL,
    aggregate_id   VARCHAR(64) NOT NULL,
    payload        JSONB NOT NULL,
    status         VARCHAR(16) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PUBLISHED')),
    attempts       INT NOT NULL DEFAULT 0,
    occurred_at    TIMESTAMPTZ NOT NULL,
    published_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_fulfillment_outbox_pending ON fulfillment.outbox_events (id) WHERE status = 'PENDING';

-- +goose Down
DROP INDEX IF EXISTS fulfillment.idx_fulfillment_outbox_pending;
DROP TABLE IF EXISTS fulfillment.outbox_events;
DROP TABLE IF EXISTS fulfillment.processed_events;
DROP TABLE IF EXISTS fulfillment.shipment_items;
DROP INDEX IF EXISTS fulfillment.idx_shipments_warehouse_status;
ALTER TABLE fulfillment.shipments DROP COLUMN IF EXISTS created_at, DROP COLUMN IF EXISTS user_id;
ALTER TABLE fulfillment.shipments ALTER COLUMN warehouse_id SET DEFAULT 'WH-HCM-01';
