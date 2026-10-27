-- =============================================================================
-- Commerce Lab Retail — Khởi tạo 9 Schema Độc lập trên 1 Database (commerce_lab)
-- Quy tắc:
--   1. Mỗi Microservice sở hữu 1 Schema + 1 DB Role riêng (khóa search_path).
--   2. CẤM Khóa ngoại liên Schema (No Cross-Schema Foreign Keys).
--   3. Bảo vệ trực tiếp các Bất biến I01–I08 bằng CHECK & UNIQUE Constraints.
-- =============================================================================

-- 1. Tạo 9 Schemas riêng biệt cho 9 Domain/Support Microservices
CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS pricing;
CREATE SCHEMA IF NOT EXISTS inventory;
CREATE SCHEMA IF NOT EXISTS orders;
CREATE SCHEMA IF NOT EXISTS payments;
CREATE SCHEMA IF NOT EXISTS fulfillment;
CREATE SCHEMA IF NOT EXISTS ledger;
CREATE SCHEMA IF NOT EXISTS support;
CREATE SCHEMA IF NOT EXISTS notifications;

-- 2. Tạo 9 DB Roles riêng và khóa search_path vào đúng Schema của từng Service
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'iam_svc_user') THEN
        CREATE USER iam_svc_user WITH PASSWORD 'iam_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'pricing_svc_user') THEN
        CREATE USER pricing_svc_user WITH PASSWORD 'pricing_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'inventory_svc_user') THEN
        CREATE USER inventory_svc_user WITH PASSWORD 'inventory_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'order_svc_user') THEN
        CREATE USER order_svc_user WITH PASSWORD 'order_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'payment_svc_user') THEN
        CREATE USER payment_svc_user WITH PASSWORD 'payment_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'fulfillment_svc_user') THEN
        CREATE USER fulfillment_svc_user WITH PASSWORD 'fulfillment_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ledger_svc_user') THEN
        CREATE USER ledger_svc_user WITH PASSWORD 'ledger_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'support_svc_user') THEN
        CREATE USER support_svc_user WITH PASSWORD 'support_secret';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'notify_svc_user') THEN
        CREATE USER notify_svc_user WITH PASSWORD 'notify_secret';
    END IF;
END
$$;

ALTER ROLE iam_svc_user SET search_path TO iam;
ALTER ROLE pricing_svc_user SET search_path TO pricing;
ALTER ROLE inventory_svc_user SET search_path TO inventory;
ALTER ROLE order_svc_user SET search_path TO orders;
ALTER ROLE payment_svc_user SET search_path TO payments;
ALTER ROLE fulfillment_svc_user SET search_path TO fulfillment;
ALTER ROLE ledger_svc_user SET search_path TO ledger;
ALTER ROLE support_svc_user SET search_path TO support;
ALTER ROLE notify_svc_user SET search_path TO notifications;

GRANT USAGE, CREATE ON SCHEMA iam TO iam_svc_user;
GRANT USAGE, CREATE ON SCHEMA pricing TO pricing_svc_user;
GRANT USAGE, CREATE ON SCHEMA inventory TO inventory_svc_user;
GRANT USAGE, CREATE ON SCHEMA orders TO order_svc_user;
GRANT USAGE, CREATE ON SCHEMA payments TO payment_svc_user;
GRANT USAGE, CREATE ON SCHEMA fulfillment TO fulfillment_svc_user;
GRANT USAGE, CREATE ON SCHEMA ledger TO ledger_svc_user;
GRANT USAGE, CREATE ON SCHEMA support TO support_svc_user;
GRANT USAGE, CREATE ON SCHEMA notifications TO notify_svc_user;

-- =============================================================================
-- SCHEMA 1: iam (iam-auth-service — B09, I08)
-- =============================================================================
CREATE TABLE IF NOT EXISTS iam.users (
    id              VARCHAR(64) PRIMARY KEY,
    email           VARCHAR(255) NOT NULL UNIQUE,
    full_name       VARCHAR(255) NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    role            VARCHAR(64) NOT NULL, -- CUSTOMER, PRODUCT_MGR, MARKETING, PROCUREMENT, WAREHOUSE_STAFF, WAREHOUSE_MGR, CSKH, CSKH_LEAD, FINANCE, OPS_MGR, ADMIN
    warehouse_scope VARCHAR(64) NOT NULL DEFAULT 'ALL', -- I08: WH-HCM-01, WH-HN-01, hoặc ALL
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SCHEMA 2: pricing (catalog-pricing-service — B01, P02, P08, I04)
-- =============================================================================
CREATE TABLE IF NOT EXISTS pricing.sku_prices (
    sku_id          VARCHAR(64) PRIMARY KEY,
    product_id      VARCHAR(64) NOT NULL,
    sku_name        VARCHAR(255) NOT NULL,
    unit_price_vnd  BIGINT NOT NULL CHECK (unit_price_vnd >= 0), -- Giá đã gồm 10% VAT
    vat_rate_pct    INT NOT NULL DEFAULT 10,
    is_published    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS pricing.quotes (
    quote_id            VARCHAR(64) PRIMARY KEY,
    user_id             VARCHAR(64) NOT NULL,
    voucher_code        VARCHAR(64),
    subtotal_vnd        BIGINT NOT NULL CHECK (subtotal_vnd >= 0),
    discount_vnd        BIGINT NOT NULL CHECK (discount_vnd >= 0),
    shipping_fee_vnd    BIGINT NOT NULL CHECK (shipping_fee_vnd >= 0),
    total_payable_vnd   BIGINT NOT NULL CHECK (total_payable_vnd >= 0),
    lines_json          JSONB NOT NULL DEFAULT '[]'::jsonb,
    version             INT NOT NULL DEFAULT 1,
    expires_at          TIMESTAMPTZ NOT NULL, -- P02: Báo giá hiệu lực 5 phút
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SCHEMA 3: inventory (inventory-warehouse-service — B02, P01, P03, P05, I01, SC02)
-- =============================================================================
CREATE TABLE IF NOT EXISTS inventory.stock_items (
    sku_id          VARCHAR(64) NOT NULL,
    warehouse_id    VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    on_hand         BIGINT NOT NULL DEFAULT 0, -- Tồn thực tế
    unsellable      BIGINT NOT NULL DEFAULT 0, -- Hỏng, cách ly, hàng trả chờ kiểm
    reserved        BIGINT NOT NULL DEFAULT 0, -- Giữ tạm chờ tiền (10p / 30p)
    committed       BIGINT NOT NULL DEFAULT 0, -- Cam kết chưa xuất
    in_transit      BIGINT NOT NULL DEFAULT 0, -- Đang chuyển kho (SC09)
    version         BIGINT NOT NULL DEFAULT 1,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (sku_id, warehouse_id),
    CONSTRAINT chk_stock_non_negative CHECK (
        on_hand >= 0 AND unsellable >= 0 AND reserved >= 0 AND committed >= 0 AND in_transit >= 0
    ),
    -- I01: Khả dụng = Tồn thực tế - Không bán được - Giữ tạm - Cam kết >= 0
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

-- =============================================================================
-- SCHEMA 4: orders (order-checkout-service — B03, B05, B08, P04, P06, P12, I02)
-- =============================================================================
CREATE TABLE IF NOT EXISTS orders.idempotency_keys (
    idempotency_key VARCHAR(128) PRIMARY KEY,
    user_id         VARCHAR(64) NOT NULL,
    request_hash    VARCHAR(128) NOT NULL, -- I02, SC04: Chống gửi lại cùng key nhưng khác giỏ hàng
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
    -- P12, SC10: Còn dùng + Đang giữ + Đã tiêu thụ không vượt tổng quota
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
    allocated_unit_paid_vnd BIGINT NOT NULL -- I04, SC07: Giá thực trả phân bổ mỗi chiếc lúc mua (VD: Balo = 270.000đ)
);

-- =============================================================================
-- SCHEMA 5: payments (payment-service — B03, P05, I03, SC05, SC08)
-- =============================================================================
CREATE TABLE IF NOT EXISTS payments.order_payment_summary (
    order_id         VARCHAR(64) PRIMARY KEY,
    expected_amount  BIGINT NOT NULL CHECK (expected_amount >= 0),
    captured_amount  BIGINT NOT NULL DEFAULT 0 CHECK (captured_amount >= 0),
    refunded_amount  BIGINT NOT NULL DEFAULT 0 CHECK (refunded_amount >= 0),
    refund_pending   BIGINT NOT NULL DEFAULT 0 CHECK (refund_pending >= 0),
    version          BIGINT NOT NULL DEFAULT 1,
    -- I03, SC08: Đã hoàn + Đang giữ chờ hoàn không vượt thực thu
    CONSTRAINT chk_refund_ceiling CHECK (
        captured_amount >= (refunded_amount + refund_pending)
    )
);

CREATE TABLE IF NOT EXISTS payments.payment_attempts (
    attempt_id       VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL,
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    amount_vnd       BIGINT NOT NULL CHECK (amount_vnd > 0),
    status           VARCHAR(32) NOT NULL, -- PROCESSING, UNKNOWN, CAPTURED, FAILED
    unknown_deadline TIMESTAMPTZ,          -- P05: Cửa sổ xác minh 30 phút
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial Unique Index: Mỗi đơn chỉ có tối đa 1 giao dịch đang PROCESSING hoặc UNKNOWN
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_payment_per_order
ON payments.payment_attempts (order_id)
WHERE status IN ('PROCESSING', 'UNKNOWN');

-- =============================================================================
-- SCHEMA 6: fulfillment (fulfillment-shipping-service — B04, B05, P06, SC06)
-- =============================================================================
CREATE TABLE IF NOT EXISTS fulfillment.shipments (
    shipment_id      VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL UNIQUE,
    warehouse_id     VARCHAR(32) NOT NULL DEFAULT 'WH-HCM-01',
    status           VARCHAR(32) NOT NULL, -- PENDING_PICK, PICKING, PACKING, STOPPED_FOR_CANCEL, HANDED_OVER, DELIVERED, DELIVERY_FAILED, RETURNING
    version          BIGINT NOT NULL DEFAULT 1,
    handed_over_at   TIMESTAMPTZ,
    delivered_at     TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SCHEMA 7: ledger (ledger-reconciliation-service — B07, I06, SC11)
-- =============================================================================
CREATE TABLE IF NOT EXISTS ledger.reconciliation_records (
    recon_id                VARCHAR(64) PRIMARY KEY,
    order_id                VARCHAR(64) NOT NULL,
    settlement_date         DATE NOT NULL,
    verified_captured_vnd   BIGINT NOT NULL, -- VD: 660.000
    partner_fee_vnd         BIGINT NOT NULL, -- VD: 6.600 (1%)
    expected_payout_vnd     BIGINT NOT NULL, -- VD: 653.400
    actual_payout_vnd       BIGINT NOT NULL, -- VD: 650.000
    discrepancy_vnd         BIGINT NOT NULL, -- VD: 3.400
    status                  VARCHAR(32) NOT NULL, -- MATCHED, DISCREPANCY_OPEN, RESOLVED
    s3_statement_key        VARCHAR(512),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- SCHEMA 8: support (backoffice-support-service — B06, B09, P07, P10, I07, SC07)
-- =============================================================================
CREATE TABLE IF NOT EXISTS support.order_line_return_ceilings (
    order_id              VARCHAR(64) NOT NULL,
    sku_id                VARCHAR(64) NOT NULL,
    delivered_qty         BIGINT NOT NULL CHECK (delivered_qty >= 0),
    returned_accepted_qty BIGINT NOT NULL DEFAULT 0 CHECK (returned_accepted_qty >= 0),
    return_open_qty       BIGINT NOT NULL DEFAULT 0 CHECK (return_open_qty >= 0),
    unit_paid_vnd         BIGINT NOT NULL CHECK (unit_paid_vnd >= 0),
    PRIMARY KEY (order_id, sku_id),
    -- I07: Lượng đã chấp nhận trả + Lượng đang mở hồ sơ trả không vượt lượng đã giao
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
    -- P10: Người tạo yêu cầu không được tự duyệt (Maker != Checker)
    CONSTRAINT chk_maker_not_checker CHECK (
        checker_user_id IS NULL OR maker_user_id <> checker_user_id
    )
);

-- =============================================================================
-- SCHEMA 9: notifications (notification-service — Idempotent Delivery)
-- =============================================================================
CREATE TABLE IF NOT EXISTS notifications.inbox_dedup_deliveries (
    event_id        VARCHAR(128) NOT NULL,
    recipient_id    VARCHAR(64) NOT NULL,
    channel         VARCHAR(32) NOT NULL, -- IN_APP, SSE, EMAIL, SMS
    delivered_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, recipient_id, channel)
);

-- =============================================================================
-- SEED DATA CHUẨN THEO KỊCH BẢN SC01 – SC12
-- =============================================================================
INSERT INTO pricing.sku_prices (sku_id, product_id, sku_name, unit_price_vnd, vat_rate_pct, is_published)
VALUES
    ('SKU-BALO-BLACK-20L', 'PROD-BALO-CITY', 'Balo City Đen 20L', 300000, 10, TRUE),
    ('SKU-BOTTLE-BLUE-500ML', 'PROD-BOTTLE-ECO', 'Bình Nước Xanh 500ml', 100000, 10, TRUE)
ON CONFLICT (sku_id) DO NOTHING;

INSERT INTO inventory.stock_items (sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit)
VALUES
    ('SKU-BALO-BLACK-20L', 'WH-HCM-01', 10, 0, 0, 0, 0),
    ('SKU-BOTTLE-BLUE-500ML', 'WH-HCM-01', 20, 0, 0, 0, 0)
ON CONFLICT (sku_id, warehouse_id) DO NOTHING;

INSERT INTO orders.voucher_campaigns (voucher_code, discount_pct, total_quota, reserved_quota, consumed_quota)
VALUES
    ('WELCOME10', 10, 100, 0, 0)
ON CONFLICT (voucher_code) DO NOTHING;

-- Seed tài khoản mẫu cho các phòng ban (Mật khẩu mặc định: 123456 -> SHA-256)
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES
    ('user-lan', 'lan@example.com', 'Khách hàng Lan', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'CUSTOMER', 'NONE', TRUE),
    ('staff-wh-hcm', 'wh.hcm@commercelab.vn', 'Nhân viên Kho HCM', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'WAREHOUSE_STAFF', 'WH-HCM-01', TRUE),
    ('staff-wh-hn', 'wh.hn@commercelab.vn', 'Nhân viên Kho HN', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'WAREHOUSE_STAFF', 'WH-HN-01', TRUE),
    ('staff-cskh', 'cskh@commercelab.vn', 'Chuyên viên CSKH', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'CSKH', 'ALL', TRUE),
    ('staff-finance', 'finance@commercelab.vn', 'Kế toán Đối soát', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'FINANCE', 'ALL', TRUE),
    ('mgr-ops', 'ops.mgr@commercelab.vn', 'Quản lý Vận hành', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'OPS_MGR', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

-- Cấp quyền thao tác bảng cho 9 DB Roles trên đúng Schema tương ứng
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA iam TO iam_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA pricing TO pricing_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA inventory TO inventory_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA orders TO order_svc_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA orders TO order_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA payments TO payment_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA fulfillment TO fulfillment_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA ledger TO ledger_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA support TO support_svc_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA notifications TO notify_svc_user;
