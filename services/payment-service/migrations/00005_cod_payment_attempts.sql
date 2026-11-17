-- +goose Up
-- P12-T02: support COD payment attempts and cash collection states
ALTER TABLE payments.payment_attempts
    DROP CONSTRAINT IF EXISTS chk_payment_attempt_status;

ALTER TABLE payments.payment_attempts
    ADD COLUMN IF NOT EXISTS payment_method VARCHAR(16) NOT NULL DEFAULT 'PREPAID',
    ADD CONSTRAINT chk_payment_attempt_status CHECK (status IN ('PROCESSING', 'UNKNOWN', 'CAPTURED', 'FAILED', 'EXPIRED_30M', 'CAPTURED_LATE', 'COD_PENDING', 'COLLECTED', 'COD_CANCELLED'));

-- +goose Down
ALTER TABLE payments.payment_attempts DROP CONSTRAINT IF EXISTS chk_payment_attempt_status;
ALTER TABLE payments.payment_attempts
    ADD CONSTRAINT chk_payment_attempt_status CHECK (status IN ('PROCESSING', 'UNKNOWN', 'CAPTURED', 'FAILED', 'EXPIRED_30M', 'CAPTURED_LATE'));
ALTER TABLE payments.payment_attempts DROP COLUMN IF EXISTS payment_method;
