-- +goose Up
-- Settlement batches and lines imported from gateway/carrier CSV reports (B07, I06, SC11)

CREATE TABLE IF NOT EXISTS ledger.settlement_batches (
    batch_id        VARCHAR(64) PRIMARY KEY,
    filename        VARCHAR(256) NOT NULL,
    s3_key          VARCHAR(512) NOT NULL DEFAULT '',
    file_hash       VARCHAR(64) NOT NULL UNIQUE,
    total_rows      INT NOT NULL DEFAULT 0,
    total_gross_vnd BIGINT NOT NULL DEFAULT 0,
    total_fee_vnd   BIGINT NOT NULL DEFAULT 0,
    total_net_vnd   BIGINT NOT NULL DEFAULT 0,
    status          VARCHAR(32) NOT NULL DEFAULT 'IMPORTED',
    imported_by     VARCHAR(64) NOT NULL DEFAULT 'SYSTEM',
    imported_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_settlement_batches_imported ON ledger.settlement_batches(imported_at DESC);

CREATE TABLE IF NOT EXISTS ledger.settlement_lines (
    line_id         BIGSERIAL PRIMARY KEY,
    batch_id        VARCHAR(64) NOT NULL REFERENCES ledger.settlement_batches(batch_id) ON DELETE CASCADE,
    line_number     INT NOT NULL,
    transaction_ref VARCHAR(64) NOT NULL,
    order_id        VARCHAR(64) NOT NULL,
    type            VARCHAR(32) NOT NULL CHECK (type IN ('COLLECTION', 'REFUND', 'FEE', 'PAYOUT')),
    gross_amount_vnd BIGINT NOT NULL,
    fee_amount_vnd   BIGINT NOT NULL DEFAULT 0,
    net_amount_vnd   BIGINT NOT NULL,
    transacted_at   TIMESTAMPTZ NOT NULL,
    match_status    VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_settlement_lines_batch_line UNIQUE (batch_id, line_number)
);

CREATE INDEX IF NOT EXISTS idx_settlement_lines_batch ON ledger.settlement_lines(batch_id);
CREATE INDEX IF NOT EXISTS idx_settlement_lines_order ON ledger.settlement_lines(order_id);
CREATE INDEX IF NOT EXISTS idx_settlement_lines_ref ON ledger.settlement_lines(transaction_ref);
CREATE INDEX IF NOT EXISTS idx_settlement_lines_date ON ledger.settlement_lines(transacted_at);

-- +goose Down
DROP TABLE IF EXISTS ledger.settlement_lines;
DROP TABLE IF EXISTS ledger.settlement_batches;
