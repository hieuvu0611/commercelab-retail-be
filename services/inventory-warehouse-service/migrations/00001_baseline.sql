-- +goose Up
-- Baseline schema for inventory (inventory-warehouse-service — B02, P01, P03, P05, I01, SC02), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS inventory.stock_items (
    sku_id          VARCHAR(64) NOT NULL,
    warehouse_id    VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    on_hand         BIGINT NOT NULL DEFAULT 0, -- Physical stock
    unsellable      BIGINT NOT NULL DEFAULT 0, -- Damaged, quarantined, returned goods awaiting inspection
    reserved        BIGINT NOT NULL DEFAULT 0, -- Temporarily held while waiting for payment (10 min / 30 min)
    committed       BIGINT NOT NULL DEFAULT 0, -- Committed but not yet shipped
    in_transit      BIGINT NOT NULL DEFAULT 0, -- Moving between warehouses (SC09)
    version         BIGINT NOT NULL DEFAULT 1,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (sku_id, warehouse_id),
    CONSTRAINT chk_stock_non_negative CHECK (
        on_hand >= 0 AND unsellable >= 0 AND reserved >= 0 AND committed >= 0 AND in_transit >= 0
    ),
    -- I01: available = on hand - unsellable - reserved - committed >= 0
    CONSTRAINT chk_available_non_negative CHECK (
        (on_hand - unsellable - reserved - committed) >= 0
    )
);

CREATE TABLE IF NOT EXISTS inventory.reservations (
    reservation_id  VARCHAR(64) PRIMARY KEY,
    order_id        VARCHAR(64) NOT NULL UNIQUE,
    warehouse_id    VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    status          VARCHAR(32) NOT NULL, -- ACTIVE_10M, EXCEPTION_HOLD_30M, COMMITTED, RELEASED, HANDED_OVER
    lines_json      JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at      TIMESTAMPTZ NOT NULL,
    version         BIGINT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS inventory.reservations;
DROP TABLE IF EXISTS inventory.stock_items;
