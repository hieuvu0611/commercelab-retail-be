-- +goose Up
CREATE TABLE IF NOT EXISTS orders.pii_erasure_tombstones (
    user_id         VARCHAR(64) PRIMARY KEY,
    erased_at       TIMESTAMPTZ NOT NULL,
    retention_until TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS orders.pii_erasure_tombstones;
