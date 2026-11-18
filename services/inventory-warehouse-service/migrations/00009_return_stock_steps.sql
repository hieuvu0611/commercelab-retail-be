-- +goose Up
CREATE TABLE inventory.return_stock_steps (
    idempotency_key TEXT PRIMARY KEY,
    warehouse_id TEXT NOT NULL,
    sku_id TEXT NOT NULL,
    on_hand_delta BIGINT NOT NULL,
    unsellable_delta BIGINT NOT NULL
);

-- +goose Down
DROP TABLE inventory.return_stock_steps;
