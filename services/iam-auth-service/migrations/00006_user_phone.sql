-- +goose Up
-- Optional contact phone number of a user (E.164 digits, normalized by the service).
ALTER TABLE iam.users ADD COLUMN IF NOT EXISTS phone VARCHAR(20);

-- +goose Down
ALTER TABLE iam.users DROP COLUMN IF EXISTS phone;
