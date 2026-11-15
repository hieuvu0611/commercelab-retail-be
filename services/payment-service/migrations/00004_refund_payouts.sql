-- +goose Up
-- P7-T03: refunds are paid out through the payment provider (the simulator in development). The obligation keeps
-- the provider's reference and the time it was paid; refunded + pending <= captured is already enforced by
-- chk_refund_ceiling on order_payment_summary (I03).
ALTER TABLE payments.refund_obligations
    ADD COLUMN IF NOT EXISTS provider_ref VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded_at  TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_refund_obligations_pending ON payments.refund_obligations (id) WHERE status = 'PENDING';

-- +goose Down
DROP INDEX IF EXISTS payments.idx_refund_obligations_pending;
ALTER TABLE payments.refund_obligations DROP COLUMN IF EXISTS refunded_at, DROP COLUMN IF EXISTS provider_ref;
