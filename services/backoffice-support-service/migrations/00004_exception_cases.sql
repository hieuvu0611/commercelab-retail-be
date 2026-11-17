-- +goose Up
-- P12-M0-T01: Persist exception cases (Section 17, I06) in PostgreSQL support schema.
CREATE TABLE IF NOT EXISTS support.exception_cases (
    case_id                 VARCHAR(64) PRIMARY KEY,
    reference_id            VARCHAR(64) NOT NULL,
    category                VARCHAR(64) NOT NULL,
    status                  VARCHAR(32) NOT NULL DEFAULT 'OPEN_UNASSIGNED',
    owner_id                VARCHAR(64),
    sla_first_assign_due_at TIMESTAMPTZ NOT NULL,
    next_action_deadline    TIMESTAMPTZ NOT NULL,
    summary                 TEXT NOT NULL,
    resolution_evidence     TEXT,
    timeline_json           JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_exception_cases_status ON support.exception_cases(status);
CREATE INDEX IF NOT EXISTS idx_exception_cases_category ON support.exception_cases(category);
CREATE INDEX IF NOT EXISTS idx_exception_cases_owner ON support.exception_cases(owner_id);

-- +goose Down
DROP TABLE IF EXISTS support.exception_cases;
