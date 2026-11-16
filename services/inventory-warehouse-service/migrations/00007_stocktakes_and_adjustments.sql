-- +goose Up
-- Stocktakes and discrepancy adjustments with maker != checker (B02 Stage 2, P10, I01, I06).
CREATE TABLE IF NOT EXISTS inventory.stocktakes (
    stocktake_id VARCHAR(64) PRIMARY KEY,
    warehouse_id VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    status       VARCHAR(32) NOT NULL DEFAULT 'IN_PROGRESS',
    started_by   VARCHAR(64) NOT NULL,
    notes        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT chk_stocktake_status CHECK (
        status IN ('IN_PROGRESS', 'SUBMITTED', 'COMPLETED', 'CANCELLED')
    )
);

CREATE TABLE IF NOT EXISTS inventory.stocktake_lines (
    id              BIGSERIAL PRIMARY KEY,
    stocktake_id    VARCHAR(64) NOT NULL REFERENCES inventory.stocktakes(stocktake_id) ON DELETE CASCADE,
    sku_id          VARCHAR(64) NOT NULL,
    system_on_hand  BIGINT NOT NULL,
    counted_on_hand BIGINT NOT NULL,
    diff_qty        BIGINT NOT NULL,
    unit_cost_vnd   BIGINT NOT NULL DEFAULT 0,
    diff_amount_vnd BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS inventory.stock_adjustments (
    adjustment_id   VARCHAR(64) PRIMARY KEY,
    stocktake_id    VARCHAR(64) NOT NULL REFERENCES inventory.stocktakes(stocktake_id),
    warehouse_id    VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    sku_id          VARCHAR(64) NOT NULL,
    diff_qty        BIGINT NOT NULL,
    unit_cost_vnd   BIGINT NOT NULL DEFAULT 0,
    diff_amount_vnd BIGINT NOT NULL DEFAULT 0,
    status          VARCHAR(32) NOT NULL DEFAULT 'PENDING_APPROVAL',
    maker_id        VARCHAR(64) NOT NULL,
    checker_id      VARCHAR(64) NOT NULL DEFAULT '',
    decided_at      TIMESTAMPTZ,
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_adjustment_status CHECK (
        status IN ('PENDING_APPROVAL', 'APPROVED', 'REJECTED')
    )
);

CREATE INDEX IF NOT EXISTS idx_stocktakes_warehouse ON inventory.stocktakes(warehouse_id);
CREATE INDEX IF NOT EXISTS idx_stocktake_lines_stocktake ON inventory.stocktake_lines(stocktake_id);
CREATE INDEX IF NOT EXISTS idx_adjustments_warehouse_status ON inventory.stock_adjustments(warehouse_id, status);

-- +goose Down
DROP TABLE IF EXISTS inventory.stock_adjustments;
DROP TABLE IF EXISTS inventory.stocktake_lines;
DROP TABLE IF EXISTS inventory.stocktakes;
