-- +goose Up
-- P12-M2-T04: Support up to 2 parcels per order across warehouses (B04).
ALTER TABLE fulfillment.shipments DROP CONSTRAINT IF EXISTS shipments_order_id_key;

ALTER TABLE fulfillment.shipments
    ADD COLUMN IF NOT EXISTS parcel_index  INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS total_parcels INT NOT NULL DEFAULT 1;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'uq_shipments_order_warehouse'
    ) THEN
        ALTER TABLE fulfillment.shipments
            ADD CONSTRAINT uq_shipments_order_warehouse UNIQUE (order_id, warehouse_id);
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_shipments_order_id ON fulfillment.shipments (order_id, parcel_index);

-- +goose Down
DROP INDEX IF EXISTS fulfillment.idx_shipments_order_id;
ALTER TABLE fulfillment.shipments DROP CONSTRAINT IF EXISTS uq_shipments_order_warehouse;
ALTER TABLE fulfillment.shipments
    DROP COLUMN IF EXISTS total_parcels,
    DROP COLUMN IF EXISTS parcel_index;
