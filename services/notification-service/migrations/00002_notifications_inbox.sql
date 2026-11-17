-- +goose Up
-- P12-M0-T02: Persist notification inbox items in PostgreSQL notifications schema.
CREATE TABLE IF NOT EXISTS notifications.inbox_items (
    notification_id VARCHAR(128) PRIMARY KEY,
    event_id        VARCHAR(128) NOT NULL,
    recipient_id    VARCHAR(64) NOT NULL,
    channel         VARCHAR(32) NOT NULL DEFAULT 'IN_APP',
    category        VARCHAR(64) NOT NULL DEFAULT '',
    title           TEXT NOT NULL,
    message         TEXT NOT NULL,
    is_read         BOOLEAN NOT NULL DEFAULT FALSE,
    metadata_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (event_id, recipient_id, channel)
);

CREATE INDEX IF NOT EXISTS idx_inbox_items_recipient_created
    ON notifications.inbox_items (recipient_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS notifications.inbox_items;
