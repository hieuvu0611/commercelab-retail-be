-- +goose Up
-- Roles, permissions and per-warehouse role grants (P1-T05). Roles follow the actors of the business blueprint
-- (docs/01 section 3). users.role / users.warehouse_scope stay for the transition; user_roles is the source of
-- truth for token claims.
CREATE TABLE IF NOT EXISTS iam.roles (
    name         VARCHAR(64)  PRIMARY KEY,
    description  VARCHAR(255) NOT NULL
);

CREATE TABLE IF NOT EXISTS iam.permissions (
    name         VARCHAR(64)  PRIMARY KEY,
    description  VARCHAR(255) NOT NULL
);

CREATE TABLE IF NOT EXISTS iam.role_permissions (
    role         VARCHAR(64) NOT NULL REFERENCES iam.roles (name),
    permission   VARCHAR(64) NOT NULL REFERENCES iam.permissions (name),
    PRIMARY KEY (role, permission)
);

-- warehouse_id NULL means the grant is not limited to one warehouse (for warehouse roles: all warehouses).
CREATE TABLE IF NOT EXISTS iam.user_roles (
    id           BIGSERIAL    PRIMARY KEY,
    user_id      VARCHAR(64)  NOT NULL REFERENCES iam.users (id),
    role         VARCHAR(64)  NOT NULL REFERENCES iam.roles (name),
    warehouse_id VARCHAR(32),
    granted_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_roles_unique ON iam.user_roles (user_id, role, COALESCE(warehouse_id, ''));
CREATE INDEX IF NOT EXISTS idx_user_roles_user ON iam.user_roles (user_id);

INSERT INTO iam.roles (name, description) VALUES
    ('CUSTOMER', 'Browses, buys, tracks and requests cancellation or return of their own orders'),
    ('PRODUCT_STAFF', 'Creates products, SKUs and content; publishes and unpublishes'),
    ('MARKETING', 'Promotional prices, vouchers and campaigns'),
    ('PROCUREMENT', 'Suppliers, purchase requests and purchase orders'),
    ('WAREHOUSE_STAFF', 'Receives, inspects, picks, packs and hands over goods in assigned warehouses'),
    ('WAREHOUSE_MGR', 'Warehouse management; approves stock adjustments requested by others'),
    ('DELIVERY_COORD', 'Tracks shipments and delivery exceptions'),
    ('SUPPORT_AGENT', 'Receives complaints, cancellation and return requests'),
    ('SUPPORT_LEAD', 'Leads support; approves within support responsibility'),
    ('FINANCE', 'Collections, approved refunds and reconciliation'),
    ('FINANCE_MGR', 'Finance management; approves finance requests'),
    ('OPS_MGR', 'Operations management; approvals and metrics'),
    ('ACCESS_ADMIN', 'Grants and revokes access; cannot edit money or inventory')
ON CONFLICT (name) DO NOTHING;

INSERT INTO iam.permissions (name, description) VALUES
    ('order:own', 'Place, track and request changes to own orders'),
    ('order:read', 'Read any order'),
    ('catalog:read', 'Read catalog data including unpublished items'),
    ('catalog:write', 'Create and change products, SKUs and content'),
    ('pricing:write', 'Change promotional prices'),
    ('campaign:manage', 'Manage vouchers and campaigns'),
    ('purchasing:manage', 'Manage suppliers, purchase requests and orders'),
    ('inventory:read', 'Read stock levels'),
    ('receiving:record', 'Record goods receipt and inspection'),
    ('fulfillment:execute', 'Pick, pack and hand over shipments'),
    ('adjustment:approve', 'Approve stock adjustments requested by someone else'),
    ('shipment:track', 'Track shipments and exceptions'),
    ('shipment:redeliver', 'Decide redelivery or return of failed deliveries'),
    ('support:case', 'Open and work support cases'),
    ('return:process', 'Process return requests'),
    ('refund:request', 'Request a refund'),
    ('refund:execute', 'Execute an approved refund'),
    ('ledger:read', 'Read the ledger'),
    ('reconciliation:manage', 'Run reconciliation and resolve differences'),
    ('approval:decide', 'Approve sensitive requests created by someone else'),
    ('report:view', 'View operational metrics'),
    ('access:manage', 'Grant and revoke roles and deactivate accounts')
ON CONFLICT (name) DO NOTHING;

INSERT INTO iam.role_permissions (role, permission) VALUES
    ('CUSTOMER', 'order:own'),
    ('PRODUCT_STAFF', 'catalog:read'), ('PRODUCT_STAFF', 'catalog:write'),
    ('MARKETING', 'catalog:read'), ('MARKETING', 'pricing:write'), ('MARKETING', 'campaign:manage'),
    ('PROCUREMENT', 'purchasing:manage'), ('PROCUREMENT', 'inventory:read'),
    ('WAREHOUSE_STAFF', 'inventory:read'), ('WAREHOUSE_STAFF', 'receiving:record'), ('WAREHOUSE_STAFF', 'fulfillment:execute'),
    ('WAREHOUSE_MGR', 'inventory:read'), ('WAREHOUSE_MGR', 'receiving:record'), ('WAREHOUSE_MGR', 'fulfillment:execute'),
    ('WAREHOUSE_MGR', 'adjustment:approve'),
    ('DELIVERY_COORD', 'shipment:track'), ('DELIVERY_COORD', 'shipment:redeliver'), ('DELIVERY_COORD', 'order:read'),
    ('SUPPORT_AGENT', 'support:case'), ('SUPPORT_AGENT', 'order:read'), ('SUPPORT_AGENT', 'return:process'), ('SUPPORT_AGENT', 'refund:request'),
    ('SUPPORT_LEAD', 'support:case'), ('SUPPORT_LEAD', 'order:read'), ('SUPPORT_LEAD', 'return:process'), ('SUPPORT_LEAD', 'refund:request'),
    ('SUPPORT_LEAD', 'approval:decide'),
    ('FINANCE', 'ledger:read'), ('FINANCE', 'refund:execute'), ('FINANCE', 'reconciliation:manage'), ('FINANCE', 'order:read'),
    ('FINANCE_MGR', 'ledger:read'), ('FINANCE_MGR', 'refund:execute'), ('FINANCE_MGR', 'reconciliation:manage'),
    ('FINANCE_MGR', 'order:read'), ('FINANCE_MGR', 'approval:decide'),
    ('OPS_MGR', 'report:view'), ('OPS_MGR', 'approval:decide'), ('OPS_MGR', 'order:read'), ('OPS_MGR', 'inventory:read'),
    ('ACCESS_ADMIN', 'access:manage')
ON CONFLICT DO NOTHING;

-- Development ACCESS_ADMIN (same documented dev password as the other seed users) so staff administration can be exercised.
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES ('staff-admin', 'admin@commercelab.vn', 'Access Administrator', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'ACCESS_ADMIN', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

-- Backfill grants from the legacy single-role columns. Legacy names are mapped to the blueprint roles
-- (CSKH -> SUPPORT_AGENT, CSKH_LEAD -> SUPPORT_LEAD, PRODUCT_MGR -> PRODUCT_STAFF, ADMIN -> ACCESS_ADMIN).
-- Warehouse roles keep their warehouse; scope ALL/NONE becomes "not limited to one warehouse".
INSERT INTO iam.user_roles (user_id, role, warehouse_id)
SELECT u.id,
       CASE u.role WHEN 'CSKH' THEN 'SUPPORT_AGENT' WHEN 'CSKH_LEAD' THEN 'SUPPORT_LEAD'
                   WHEN 'PRODUCT_MGR' THEN 'PRODUCT_STAFF' WHEN 'ADMIN' THEN 'ACCESS_ADMIN' ELSE u.role END,
       CASE WHEN u.role IN ('WAREHOUSE_STAFF', 'WAREHOUSE_MGR') AND u.warehouse_scope NOT IN ('ALL', 'NONE')
            THEN u.warehouse_scope END
FROM iam.users u
WHERE CASE u.role WHEN 'CSKH' THEN 'SUPPORT_AGENT' WHEN 'CSKH_LEAD' THEN 'SUPPORT_LEAD'
                  WHEN 'PRODUCT_MGR' THEN 'PRODUCT_STAFF' WHEN 'ADMIN' THEN 'ACCESS_ADMIN' ELSE u.role END
      IN (SELECT name FROM iam.roles)
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS iam.user_roles;
DROP TABLE IF EXISTS iam.role_permissions;
DROP TABLE IF EXISTS iam.permissions;
DROP TABLE IF EXISTS iam.roles;
DELETE FROM iam.users WHERE id = 'staff-admin';
