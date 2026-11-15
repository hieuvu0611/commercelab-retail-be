-- +goose Up
-- P6-T04: simulated carrier notices (B04). delivered_at already exists; the seven-day return window (P07) and
-- the delivery failure reason are new. carrier_events dedupes notices by the carrier's event id.
ALTER TABLE fulfillment.shipments
    ADD COLUMN IF NOT EXISTS return_window_ends_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS failure_reason        VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE fulfillment.shipment_items
    ADD COLUMN IF NOT EXISTS delivered_quantity BIGINT NOT NULL DEFAULT 0 CHECK (delivered_quantity >= 0);

CREATE TABLE IF NOT EXISTS fulfillment.carrier_events (
    event_id    VARCHAR(64) PRIMARY KEY,
    shipment_id VARCHAR(64) NOT NULL REFERENCES fulfillment.shipments (shipment_id) ON DELETE CASCADE,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS fulfillment.carrier_events;
ALTER TABLE fulfillment.shipment_items DROP COLUMN IF EXISTS delivered_quantity;
ALTER TABLE fulfillment.shipments DROP COLUMN IF EXISTS failure_reason, DROP COLUMN IF EXISTS return_window_ends_at;
