-- +goose Up
-- Baseline schema for orders (order-checkout-service — B03, B05, B08, P04, P06, P12, I02), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS orders.idempotency_keys (
    idempotency_key VARCHAR(128) PRIMARY KEY,
    user_id         VARCHAR(64) NOT NULL,
    request_hash    VARCHAR(128) NOT NULL, -- I02, SC04: rejects the same key reused with a different cart
    order_id        VARCHAR(64) NOT NULL,
    response_json   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS orders.voucher_campaigns (
    voucher_code    VARCHAR(64) PRIMARY KEY,
    discount_pct    INT NOT NULL CHECK (discount_pct BETWEEN 1 AND 100),
    total_quota     BIGINT NOT NULL CHECK (total_quota >= 0),
    reserved_quota  BIGINT NOT NULL DEFAULT 0 CHECK (reserved_quota >= 0),
    consumed_quota  BIGINT NOT NULL DEFAULT 0 CHECK (consumed_quota >= 0),
    version         BIGINT NOT NULL DEFAULT 1,
    -- P12, SC10: reserved + consumed never exceeds the total quota
    CONSTRAINT chk_voucher_quota_ceiling CHECK (
        total_quota >= (reserved_quota + consumed_quota)
    )
);

CREATE TABLE IF NOT EXISTS orders.orders (
    order_id            VARCHAR(64) PRIMARY KEY,
    user_id             VARCHAR(64) NOT NULL,
    quote_id            VARCHAR(64) NOT NULL,
    voucher_code        VARCHAR(64),
    status              VARCHAR(32) NOT NULL, -- PENDING, CONFIRMED, IN_FULFILLMENT, CANCEL_REQUESTED, DELIVERED, CANCELLED, FAILED_CLOSED
    payment_status      VARCHAR(32) NOT NULL, -- UNPAID, PROCESSING, UNKNOWN_30M, CAPTURED, FAILED, REFUND_PENDING, PARTIALLY_REFUNDED, REFUNDED
    subtotal_vnd        BIGINT NOT NULL,
    discount_vnd        BIGINT NOT NULL,
    shipping_fee_vnd    BIGINT NOT NULL,
    total_payable_vnd   BIGINT NOT NULL,
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS orders.order_items (
    id                      BIGSERIAL PRIMARY KEY,
    order_id                VARCHAR(64) NOT NULL,
    sku_id                  VARCHAR(64) NOT NULL,
    sku_name                VARCHAR(255) NOT NULL,
    quantity                BIGINT NOT NULL CHECK (quantity > 0),
    unit_price_vnd          BIGINT NOT NULL,
    line_discount_vnd       BIGINT NOT NULL,
    allocated_unit_paid_vnd BIGINT NOT NULL -- I04, SC07: allocated amount actually paid per unit at purchase (e.g. backpack = 270,000 VND)
);

-- +goose Down
DROP TABLE IF EXISTS orders.order_items;
DROP TABLE IF EXISTS orders.orders;
DROP TABLE IF EXISTS orders.voucher_campaigns;
DROP TABLE IF EXISTS orders.idempotency_keys;
