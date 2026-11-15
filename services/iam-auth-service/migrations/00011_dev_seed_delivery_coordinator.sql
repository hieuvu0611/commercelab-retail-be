-- +goose Up
-- Development delivery coordinator so delivery exceptions (GET /admin/fulfillment-cases?type=DELIVERY_COORD) can be
-- exercised against the real stack. Signs in with the documented development password (DEV_SEED_PASSWORD).
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES
    ('staff-delivery-coord', 'delivery.coord@commercelab.vn', 'Delivery Coordinator', '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk', 'DELIVERY_COORD', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam.user_roles (user_id, role, warehouse_id)
VALUES ('staff-delivery-coord', 'DELIVERY_COORD', NULL)
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM iam.user_roles WHERE user_id = 'staff-delivery-coord';
DELETE FROM iam.users WHERE id = 'staff-delivery-coord';
