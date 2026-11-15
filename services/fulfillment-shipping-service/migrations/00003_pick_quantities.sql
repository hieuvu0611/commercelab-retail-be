-- +goose Up
-- P6-T02: pick progress per line (B04: scan verifies SKU and quantity).
ALTER TABLE fulfillment.shipment_items
    ADD COLUMN IF NOT EXISTS picked_quantity BIGINT NOT NULL DEFAULT 0 CHECK (picked_quantity >= 0),
    ADD CONSTRAINT chk_picked_within_ordered CHECK (picked_quantity <= quantity);

-- +goose Down
ALTER TABLE fulfillment.shipment_items DROP CONSTRAINT IF EXISTS chk_picked_within_ordered, DROP COLUMN IF EXISTS picked_quantity;
