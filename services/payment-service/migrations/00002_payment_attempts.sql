-- +goose Up
-- P5-T05: payment attempts of the checkout saga (B03, P04, P05, SC05).
ALTER TABLE payments.payment_attempts
    ADD COLUMN IF NOT EXISTS user_id             VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS captured_amount_vnd BIGINT NOT NULL DEFAULT 0 CHECK (captured_amount_vnd >= 0),
    ADD COLUMN IF NOT EXISTS outcome_at          TIMESTAMPTZ;

-- Statuses: PROCESSING, UNKNOWN, CAPTURED, FAILED, EXPIRED_30M, CAPTURED_LATE (captured after the order closed).
ALTER TABLE payments.payment_attempts
    ADD CONSTRAINT chk_payment_attempt_status CHECK (status IN ('PROCESSING', 'UNKNOWN', 'CAPTURED', 'FAILED', 'EXPIRED_30M', 'CAPTURED_LATE'));

-- One attempt per order: a failed payment closes the order and the customer places a new one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_attempt_order ON payments.payment_attempts (order_id);

-- Money that arrived after the order was closed and must go back to the customer (SC05).
CREATE TABLE IF NOT EXISTS payments.refund_obligations (
    id          BIGSERIAL PRIMARY KEY,
    order_id    VARCHAR(64) NOT NULL,
    attempt_id  VARCHAR(64) NOT NULL UNIQUE REFERENCES payments.payment_attempts (attempt_id),
    amount_vnd  BIGINT NOT NULL CHECK (amount_vnd > 0),
    reason      VARCHAR(32) NOT NULL DEFAULT 'LATE_CAPTURE',
    status      VARCHAR(16) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DONE')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Webhook event ids already applied, so a replayed delivery changes nothing.
CREATE TABLE IF NOT EXISTS payments.webhook_events (
    event_id    VARCHAR(128) PRIMARY KEY,
    attempt_id  VARCHAR(64) NOT NULL,
    outcome     VARCHAR(16) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS payments.webhook_events;
DROP TABLE IF EXISTS payments.refund_obligations;
DROP INDEX IF EXISTS payments.idx_payment_attempt_order;
ALTER TABLE payments.payment_attempts DROP CONSTRAINT IF EXISTS chk_payment_attempt_status;
ALTER TABLE payments.payment_attempts
    DROP COLUMN IF EXISTS outcome_at,
    DROP COLUMN IF EXISTS captured_amount_vnd,
    DROP COLUMN IF EXISTS user_id;
