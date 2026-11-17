-- +goose Up
-- P12-T04: Chart of accounts for COD receivables and carrier fees
INSERT INTO ledger.accounts (account_code, account_name, account_type, balance_vnd)
VALUES
    ('1310', '3PL COD Receivables (Carrier Cash Collection)', 'ASSET', 0),
    ('6420', 'Carrier COD Processing Fees', 'EXPENSE', 0)
ON CONFLICT (account_code) DO NOTHING;

-- +goose Down
DELETE FROM ledger.accounts WHERE account_code IN ('1310', '6420');
