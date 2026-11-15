-- +goose Up
-- Development warehouse managers so opening stock and the scoped-access rules can be exercised against the real stack.
-- whmgr.hcm is limited to WH-HCM-01; whmgr.all manages every warehouse (a warehouse role granted without a warehouse).
-- They sign in with the documented development password (DEV_SEED_PASSWORD in .env.example).
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES
    ('staff-whm-hcm', 'whmgr.hcm@commercelab.vn', 'Warehouse Manager HCM', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'WAREHOUSE_MGR', 'WH-HCM-01', TRUE),
    ('staff-whm-all', 'whmgr.all@commercelab.vn', 'Warehouse Manager All', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'WAREHOUSE_MGR', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam.user_roles (user_id, role, warehouse_id)
VALUES ('staff-whm-hcm', 'WAREHOUSE_MGR', 'WH-HCM-01'), ('staff-whm-all', 'WAREHOUSE_MGR', NULL)
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM iam.user_roles WHERE user_id IN ('staff-whm-hcm', 'staff-whm-all');
DELETE FROM iam.users WHERE id IN ('staff-whm-hcm', 'staff-whm-all');
