-- +goose Up
-- Audit trail of authentication and access-administration events (P1-T09, reused by P1-T08).
-- Never store raw email addresses or phone numbers here: reference users by id and keep details to role names.
CREATE TABLE IF NOT EXISTS iam.auth_audit_logs (
    id              BIGSERIAL    PRIMARY KEY,
    event           VARCHAR(64)  NOT NULL,
    actor_user_id   VARCHAR(64),
    target_user_id  VARCHAR(64),
    ip              VARCHAR(64),
    user_agent      VARCHAR(255),
    outcome         VARCHAR(16)  NOT NULL DEFAULT 'SUCCESS',
    reason          VARCHAR(500),
    detail          JSONB        NOT NULL DEFAULT '{}'::jsonb,
    at              TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_auth_audit_logs_target ON iam.auth_audit_logs (target_user_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_audit_logs_actor ON iam.auth_audit_logs (actor_user_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS iam.auth_audit_logs;
