-- +goose Up
ALTER TABLE fulfillment.shipments ADD COLUMN stop_confirmed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE fulfillment.shipments DROP COLUMN stop_confirmed_at;
