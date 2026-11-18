-- +goose Up
-- Reconciliation cases for financial discrepancies (B07, I06, SC11)

CREATE TABLE IF NOT EXISTS ledger.reconciliation_cases (
    case_id             VARCHAR(64) PRIMARY KEY,
    batch_id            VARCHAR(64) NOT NULL REFERENCES ledger.settlement_batches(batch_id) ON DELETE CASCADE,
    line_number         INT NOT NULL,
    order_id            VARCHAR(64) NOT NULL,
    transaction_ref     VARCHAR(64) NOT NULL,
    expected_payout_vnd BIGINT NOT NULL,
    actual_payout_vnd   BIGINT NOT NULL,
    discrepancy_vnd     BIGINT NOT NULL,
    cause_category      VARCHAR(64) NOT NULL DEFAULT 'UNCLASSIFIED',
    status              VARCHAR(32) NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED', 'ESCALATED', 'CLOSED')),
    assigned_to         VARCHAR(64) NOT NULL DEFAULT '',
    resolved_by         VARCHAR(64) NOT NULL DEFAULT '',
    resolution_notes    TEXT NOT NULL DEFAULT '',
    resolved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_reconciliation_cases_batch_line UNIQUE (batch_id, line_number)
);

CREATE INDEX IF NOT EXISTS idx_reconciliation_cases_status ON ledger.reconciliation_cases(status);
CREATE INDEX IF NOT EXISTS idx_reconciliation_cases_order ON ledger.reconciliation_cases(order_id);
CREATE INDEX IF NOT EXISTS idx_reconciliation_cases_batch ON ledger.reconciliation_cases(batch_id);

-- +goose Down
DROP TABLE IF EXISTS ledger.reconciliation_cases;
