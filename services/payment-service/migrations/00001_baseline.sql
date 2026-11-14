-- +goose Up
-- Baseline schema for payments (payment-service — B03, P05, I03, SC05, SC08), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS payments.order_payment_summary (
    order_id         VARCHAR(64) PRIMARY KEY,
    expected_amount  BIGINT NOT NULL CHECK (expected_amount >= 0),
    captured_amount  BIGINT NOT NULL DEFAULT 0 CHECK (captured_amount >= 0),
    refunded_amount  BIGINT NOT NULL DEFAULT 0 CHECK (refunded_amount >= 0),
    refund_pending   BIGINT NOT NULL DEFAULT 0 CHECK (refund_pending >= 0),
    version          BIGINT NOT NULL DEFAULT 1,
    -- I03, SC08: refunded + refund pending never exceeds captured
    CONSTRAINT chk_refund_ceiling CHECK (
        captured_amount >= (refunded_amount + refund_pending)
    )
);

CREATE TABLE IF NOT EXISTS payments.payment_attempts (
    attempt_id       VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL,
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    amount_vnd       BIGINT NOT NULL CHECK (amount_vnd > 0),
    status           VARCHAR(32) NOT NULL, -- PROCESSING, UNKNOWN, CAPTURED, FAILED
    unknown_deadline TIMESTAMPTZ,          -- P05: 30-minute verification window
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial unique index: at most one PROCESSING or UNKNOWN attempt per order
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_payment_per_order
ON payments.payment_attempts (order_id)
WHERE status IN ('PROCESSING', 'UNKNOWN');

-- +goose Down
DROP TABLE IF EXISTS payments.payment_attempts;
DROP TABLE IF EXISTS payments.order_payment_summary;
