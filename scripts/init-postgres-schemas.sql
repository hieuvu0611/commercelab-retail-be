-- =============================================================================
-- Commerce Lab Retail — database bootstrap (runs once on a fresh Postgres volume)
-- Creates the nine service schemas, their DB roles and schema-level grants only.
-- Tables, constraints and seed data live in versioned goose migrations under
-- services/<svc>/migrations/ and are applied with `make migrate` (or MIGRATE_ON_START=true).
-- Rules: one schema + one role per service (search_path pinned); no cross-schema foreign keys.
-- =============================================================================

-- 1. One schema per service
CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS pricing;
CREATE SCHEMA IF NOT EXISTS inventory;
CREATE SCHEMA IF NOT EXISTS orders;
CREATE SCHEMA IF NOT EXISTS payments;
CREATE SCHEMA IF NOT EXISTS fulfillment;
CREATE SCHEMA IF NOT EXISTS ledger;
CREATE SCHEMA IF NOT EXISTS support;
CREATE SCHEMA IF NOT EXISTS notifications;

-- 2. One DB role per service, with search_path pinned to its own schema
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
