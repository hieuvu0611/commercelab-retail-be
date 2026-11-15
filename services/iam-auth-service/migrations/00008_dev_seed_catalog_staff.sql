-- +goose Up
-- Development catalog staff so the product and pricing flows can be exercised against the real stack.
-- They sign in with the documented development password (DEV_SEED_PASSWORD in .env.example).
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES
    ('staff-product', 'product@commercelab.vn', 'Product Staff', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'PRODUCT_STAFF', 'ALL', TRUE),
    ('staff-marketing', 'marketing@commercelab.vn', 'Marketing Staff', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'MARKETING', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam.user_roles (user_id, role, warehouse_id)
VALUES ('staff-product', 'PRODUCT_STAFF', NULL), ('staff-marketing', 'MARKETING', NULL)
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM iam.user_roles WHERE user_id IN ('staff-product', 'staff-marketing');
DELETE FROM iam.users WHERE id IN ('staff-product', 'staff-marketing');
