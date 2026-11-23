-- +goose Up
ALTER TABLE iam.users
    ADD COLUMN IF NOT EXISTS deleted_at    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS anonymized_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS iam.pii_erasure_tombstones (
    user_id          VARCHAR(64) PRIMARY KEY REFERENCES iam.users(id) ON DELETE CASCADE,
    anonymized_email VARCHAR(255) NOT NULL,
    erased_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retention_until  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS iam.outbox_events (
    id             BIGSERIAL PRIMARY KEY,
    event_id       VARCHAR(64) NOT NULL UNIQUE,
    event_type     VARCHAR(128) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id   VARCHAR(64) NOT NULL,
    payload        JSONB NOT NULL,
    status         VARCHAR(16) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PUBLISHED')),
    attempts       INT NOT NULL DEFAULT 0,
    occurred_at    TIMESTAMPTZ NOT NULL,
    published_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_iam_outbox_pending ON iam.outbox_events (id) WHERE status = 'PENDING';

-- +goose Down
DROP INDEX IF EXISTS iam.idx_iam_outbox_pending;
DROP TABLE IF EXISTS iam.outbox_events;
DROP TABLE IF EXISTS iam.pii_erasure_tombstones;
ALTER TABLE iam.users
    DROP COLUMN IF EXISTS anonymized_at,
    DROP COLUMN IF EXISTS deleted_at;
