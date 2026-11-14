-- +goose Up
-- Baseline schema for notifications (notification-service — Idempotent Delivery), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS notifications.inbox_dedup_deliveries (
    event_id        VARCHAR(128) NOT NULL,
    recipient_id    VARCHAR(64) NOT NULL,
    channel         VARCHAR(32) NOT NULL, -- IN_APP, SSE, EMAIL, SMS
    delivered_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, recipient_id, channel)
);

-- +goose Down
DROP TABLE IF EXISTS notifications.inbox_dedup_deliveries;
