-- +goose Up
-- Persisted refund reservations for RMA refunds within captured ceiling (I03, SC08).
CREATE TABLE IF NOT EXISTS payments.refund_reservations (
    reservation_id VARCHAR(64) PRIMARY KEY,
    order_id       VARCHAR(64) NOT NULL,
    amount_vnd     BIGINT NOT NULL CHECK (amount_vnd > 0),
    status         VARCHAR(16) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DONE')),
    provider_ref   VARCHAR(64) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_refund_reservations_order ON payments.refund_reservations (order_id);

-- +goose Down
DROP INDEX IF EXISTS payments.idx_refund_reservations_order;
DROP TABLE IF EXISTS payments.refund_reservations;
