-- +goose Up
-- Replace the SHA-256 hashes of the development seed users with Argon2id hashes of the development password
-- (see DEV_SEED_PASSWORD in .env.example). Only rows that still hold the old seed hash are touched.
UPDATE iam.users
SET password_hash = '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk'
WHERE id IN ('user-lan', 'staff-wh-hcm', 'staff-wh-hn', 'staff-cskh', 'staff-finance', 'mgr-ops')
  AND password_hash = '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92';

-- +goose Down
UPDATE iam.users
SET password_hash = '8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92'
WHERE id IN ('user-lan', 'staff-wh-hcm', 'staff-wh-hn', 'staff-cskh', 'staff-finance', 'mgr-ops')
  AND password_hash = '$argon2id$v=19$m=19456,t=2,p=1$Y29tbWVyY2VsYWItZGV2IQ$T0z8uA0OLoGs98KpaLlVwoIuImdW9D73LWf17+VAaKk';
