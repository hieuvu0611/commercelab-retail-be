-- +goose Up
-- Baseline schema for iam (iam-auth-service — B09, I08), moved from scripts/init-postgres-schemas.sql.
CREATE TABLE IF NOT EXISTS iam.users (
    id              VARCHAR(64) PRIMARY KEY,
    email           VARCHAR(255) NOT NULL UNIQUE,
    full_name       VARCHAR(255) NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    role            VARCHAR(64) NOT NULL, -- CUSTOMER, PRODUCT_MGR, MARKETING, PROCUREMENT, WAREHOUSE_STAFF, WAREHOUSE_MGR, CSKH, CSKH_LEAD, FINANCE, OPS_MGR, ADMIN
    warehouse_scope VARCHAR(64) NOT NULL DEFAULT 'ALL', -- I08: WH-HCM-01, WH-HN-01 or ALL
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS iam.users;
