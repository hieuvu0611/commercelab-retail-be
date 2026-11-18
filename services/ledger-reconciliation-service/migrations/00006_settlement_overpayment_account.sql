-- +goose Up
INSERT INTO ledger.accounts (account_code, account_name, account_type, balance_vnd)
VALUES ('3389', 'Settlement Overpayments Payable', 'LIABILITY', 0)
ON CONFLICT (account_code) DO NOTHING;

-- +goose Down
DELETE FROM ledger.accounts WHERE account_code = '3389';
