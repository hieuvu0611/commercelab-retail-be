-- +goose Up
-- Baseline schema for support (backoffice-support-service — B06, B09, P07, P10, I07, SC07), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS support.order_line_return_ceilings (
    order_id              VARCHAR(64) NOT NULL,
    sku_id                VARCHAR(64) NOT NULL,
    delivered_qty         BIGINT NOT NULL CHECK (delivered_qty >= 0),
    returned_accepted_qty BIGINT NOT NULL DEFAULT 0 CHECK (returned_accepted_qty >= 0),
    return_open_qty       BIGINT NOT NULL DEFAULT 0 CHECK (return_open_qty >= 0),
    unit_paid_vnd         BIGINT NOT NULL CHECK (unit_paid_vnd >= 0),
    PRIMARY KEY (order_id, sku_id),
    -- I07: accepted returns + open return requests never exceed delivered quantity
    CONSTRAINT chk_return_qty_ceiling CHECK (
        delivered_qty >= (returned_accepted_qty + return_open_qty)
    )
);

CREATE TABLE IF NOT EXISTS support.approval_requests (
    approval_id     VARCHAR(64) PRIMARY KEY,
    request_type    VARCHAR(64) NOT NULL, -- EXCEPTION_REFUND, STOCK_ADJUSTMENT, PURCHASE_ORDER
    reference_id    VARCHAR(64) NOT NULL,
    amount_vnd      BIGINT NOT NULL DEFAULT 0,
    quantity_diff   BIGINT NOT NULL DEFAULT 0,
    maker_user_id   VARCHAR(64) NOT NULL,
    checker_user_id VARCHAR(64),
    status          VARCHAR(32) NOT NULL DEFAULT 'PENDING', -- PENDING, APPROVED, REJECTED, EXECUTED
    reason          TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- P10: the requester cannot approve their own request (maker != checker)
    CONSTRAINT chk_maker_not_checker CHECK (
        checker_user_id IS NULL OR maker_user_id <> checker_user_id
    )
);

-- +goose Down
DROP TABLE IF EXISTS support.approval_requests;
DROP TABLE IF EXISTS support.order_line_return_ceilings;
