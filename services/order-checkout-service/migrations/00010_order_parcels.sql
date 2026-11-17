-- +goose Up
-- P12-M2-T03: Persist multi-warehouse parcel allocations (max 2 parcels/order) for split fulfillment (B04).
CREATE TABLE IF NOT EXISTS orders.order_parcels (
    order_id       VARCHAR(64) NOT NULL REFERENCES orders.orders(order_id) ON DELETE CASCADE,
    parcel_seq     INT NOT NULL CHECK (parcel_seq >= 1 AND parcel_seq <= 2),
    warehouse_id   VARCHAR(32) NOT NULL,
    items_json     JSONB NOT NULL DEFAULT '[]'::jsonb,
    cod_amount_vnd BIGINT NOT NULL DEFAULT 0 CHECK (cod_amount_vnd >= 0),
    status         VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (order_id, parcel_seq),
    UNIQUE (order_id, warehouse_id)
);

CREATE INDEX IF NOT EXISTS idx_order_parcels_wh ON orders.order_parcels (warehouse_id, order_id);

-- +goose Down
DROP TABLE IF EXISTS orders.order_parcels;
