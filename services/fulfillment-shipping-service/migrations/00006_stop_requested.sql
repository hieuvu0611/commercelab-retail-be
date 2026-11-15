-- +goose Up
-- P7-T02: the customer's cancellation asks the warehouse to stop a parcel that is being picked or packed (SC06).
ALTER TABLE fulfillment.shipments ADD COLUMN IF NOT EXISTS stop_requested_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE fulfillment.shipments DROP COLUMN IF EXISTS stop_requested_at;
