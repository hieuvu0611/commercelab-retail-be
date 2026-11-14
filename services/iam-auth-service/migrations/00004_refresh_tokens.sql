-- +goose Up
-- Stored refresh tokens (D-03): only a SHA-256 of the token is kept, every refresh rotates the token inside a
-- family, and reuse of an already-rotated token revokes the whole family.
CREATE TABLE IF NOT EXISTS iam.refresh_tokens (
    id           VARCHAR(64)  PRIMARY KEY,
    user_id      VARCHAR(64)  NOT NULL,
    family_id    VARCHAR(64)  NOT NULL,
    token_hash   CHAR(64)     NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ  NOT NULL,
    revoked_at   TIMESTAMPTZ,
    replaced_by  VARCHAR(64),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON iam.refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON iam.refresh_tokens (user_id);

-- +goose Down
DROP TABLE IF EXISTS iam.refresh_tokens;
