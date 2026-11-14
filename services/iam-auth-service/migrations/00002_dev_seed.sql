-- +goose Up
-- Development seed users (default password: 123456, SHA-256). Replaced by Argon2id in P1-T01.
INSERT INTO iam.users (id, email, full_name, password_hash, role, warehouse_scope, is_active)
VALUES
    ('user-lan', 'lan@example.com', 'Khách hàng Lan', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'CUSTOMER', 'NONE', TRUE),
    ('staff-wh-hcm', 'wh.hcm@commercelab.vn', 'Nhân viên Kho HCM', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'WAREHOUSE_STAFF', 'WH-HCM-01', TRUE),
    ('staff-wh-hn', 'wh.hn@commercelab.vn', 'Nhân viên Kho HN', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'WAREHOUSE_STAFF', 'WH-HN-01', TRUE),
    ('staff-cskh', 'cskh@commercelab.vn', 'Chuyên viên CSKH', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'CSKH', 'ALL', TRUE),
    ('staff-finance', 'finance@commercelab.vn', 'Kế toán Đối soát', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'FINANCE', 'ALL', TRUE),
    ('mgr-ops', 'ops.mgr@commercelab.vn', 'Quản lý Vận hành', '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92', 'OPS_MGR', 'ALL', TRUE)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM iam.users WHERE id IN ('user-lan', 'staff-wh-hcm', 'staff-wh-hn', 'staff-cskh', 'staff-finance', 'mgr-ops');
