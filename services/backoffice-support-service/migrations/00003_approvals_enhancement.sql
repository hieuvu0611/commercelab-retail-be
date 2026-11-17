-- +goose Up
ALTER TABLE support.approval_requests ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE support.approval_requests ADD COLUMN IF NOT EXISTS decision_note TEXT;

-- +goose Down
ALTER TABLE support.approval_requests DROP COLUMN IF EXISTS decision_note;
ALTER TABLE support.approval_requests DROP COLUMN IF EXISTS updated_at;
