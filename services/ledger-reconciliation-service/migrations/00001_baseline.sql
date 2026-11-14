-- +goose Up
-- Baseline schema for ledger (ledger-reconciliation-service — B07, I06, SC11), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS ledger.reconciliation_records (
    recon_id                VARCHAR(64) PRIMARY KEY,
    order_id                VARCHAR(64) NOT NULL,
    settlement_date         DATE NOT NULL,
    verified_captured_vnd   BIGINT NOT NULL, -- VD: 660.000
    partner_fee_vnd         BIGINT NOT NULL, -- VD: 6.600 (1%)
    expected_payout_vnd     BIGINT NOT NULL, -- VD: 653.400
    actual_payout_vnd       BIGINT NOT NULL, -- VD: 650.000
    discrepancy_vnd         BIGINT NOT NULL, -- VD: 3.400
    status                  VARCHAR(32) NOT NULL, -- MATCHED, DISCREPANCY_OPEN, RESOLVED
    s3_statement_key        VARCHAR(512),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS ledger.reconciliation_records;
