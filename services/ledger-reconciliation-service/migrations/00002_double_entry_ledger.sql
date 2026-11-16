-- +goose Up
-- Double-entry ledger: accounts, journal entries, and postings (B07, I06, SC11)

CREATE TABLE IF NOT EXISTS ledger.accounts (
    account_code    VARCHAR(32) PRIMARY KEY,
    account_name    VARCHAR(128) NOT NULL,
    account_type    VARCHAR(32) NOT NULL CHECK (account_type IN ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE')),
    balance_vnd     BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed standard Chart of Accounts
INSERT INTO ledger.accounts (account_code, account_name, account_type, balance_vnd)
VALUES
    ('1110', 'Cash in Bank (Settlement Account)', 'ASSET', 0),
    ('1120', 'Payment Gateway Receivable (VNPay / Stripe)', 'ASSET', 0),
    ('1388', 'Reconciliation Discrepancies Receivable', 'ASSET', 0),
    ('3388', 'Customer Refund Obligations', 'LIABILITY', 0),
    ('5110', 'Gross Sales Revenue', 'REVENUE', 0),
    ('5210', 'Sales Returns & Deductions', 'REVENUE', 0),
    ('6410', 'Payment Gateway Processing Fees', 'EXPENSE', 0)
ON CONFLICT (account_code) DO NOTHING;

CREATE TABLE IF NOT EXISTS ledger.journal_entries (
    entry_id        VARCHAR(64) PRIMARY KEY,
    event_type      VARCHAR(64) NOT NULL, -- PAYMENT_CAPTURED, REFUND_ISSUED, SETTLEMENT_MATCHED, ADJUSTMENT
    reference_id    VARCHAR(64) NOT NULL, -- Order ID, Refund ID, Settlement Batch ID
    description     TEXT NOT NULL DEFAULT '',
    posted_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by      VARCHAR(64) NOT NULL DEFAULT 'SYSTEM',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_journal_entries_ref ON ledger.journal_entries(reference_id);
CREATE INDEX IF NOT EXISTS idx_journal_entries_posted ON ledger.journal_entries(posted_at);

CREATE TABLE IF NOT EXISTS ledger.ledger_postings (
    posting_id      BIGSERIAL PRIMARY KEY,
    entry_id        VARCHAR(64) NOT NULL REFERENCES ledger.journal_entries(entry_id) ON DELETE CASCADE,
    account_code    VARCHAR(32) NOT NULL REFERENCES ledger.accounts(account_code),
    direction       VARCHAR(16) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount_vnd      BIGINT NOT NULL CHECK (amount_vnd > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_postings_entry ON ledger.ledger_postings(entry_id);
CREATE INDEX IF NOT EXISTS idx_ledger_postings_account ON ledger.ledger_postings(account_code);

-- +goose Down
DROP TABLE IF EXISTS ledger.ledger_postings;
DROP TABLE IF EXISTS ledger.journal_entries;
DROP TABLE IF EXISTS ledger.accounts;
