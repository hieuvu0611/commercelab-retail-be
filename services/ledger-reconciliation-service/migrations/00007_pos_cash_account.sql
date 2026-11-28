-- +goose Up
INSERT INTO ledger.accounts (account_code, account_name, account_type, balance_vnd)
VALUES ('1111', 'Store Cash on Hand (POS Register)', 'ASSET', 0)
ON CONFLICT (account_code) DO NOTHING;

-- +goose Down
DELETE FROM ledger.accounts WHERE account_code = '1111';
