-- +goose Up
-- Delivery address book of a customer (P4-T02). Contact phone and address text are personal data: never log them.
CREATE TABLE IF NOT EXISTS iam.customer_addresses (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL REFERENCES iam.users (id) ON DELETE CASCADE,
    recipient   VARCHAR(100) NOT NULL,
    phone       VARCHAR(20) NOT NULL,
    line1       VARCHAR(200) NOT NULL,
    ward        VARCHAR(100) NOT NULL,
    district    VARCHAR(100) NOT NULL,
    province    VARCHAR(100) NOT NULL,
    is_default  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customer_addresses_user ON iam.customer_addresses (user_id);
-- At most one default address per user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_addresses_default ON iam.customer_addresses (user_id) WHERE is_default;

-- +goose Down
DROP TABLE IF EXISTS iam.customer_addresses;
