-- +goose Up
-- P5-T08: transactional outbox (docs/04 7.5). A row is written in the same transaction as the state change it
-- announces; the relay publishes PENDING rows to Redpanda and marks them PUBLISHED. Consumers dedupe by event_id.
CREATE TABLE IF NOT EXISTS orders.outbox_events (
    id             BIGSERIAL PRIMARY KEY,
    event_id       VARCHAR(64) NOT NULL UNIQUE,
    event_type     VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(32) NOT NULL,
    aggregate_id   VARCHAR(64) NOT NULL,
    payload        JSONB NOT NULL,
    status         VARCHAR(16) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PUBLISHED')),
    attempts       INT NOT NULL DEFAULT 0,
    occurred_at    TIMESTAMPTZ NOT NULL,
    published_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orders_outbox_pending ON orders.outbox_events (id) WHERE status = 'PENDING';

-- +goose Down
DROP INDEX IF EXISTS orders.idx_orders_outbox_pending;
DROP TABLE IF EXISTS orders.outbox_events;
