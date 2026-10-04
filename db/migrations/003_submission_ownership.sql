-- Do not backfill ownership: legacy rows must stay inaccessible to scoped APIs.
ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS owner_service TEXT,
    ADD COLUMN IF NOT EXISTS owner_user TEXT;

CREATE INDEX IF NOT EXISTS submissions_active_owner_idx
    ON submissions (owner_service, owner_user)
    WHERE status IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS submissions_terminal_updated_at_idx
    ON submissions (updated_at, id)
    WHERE status IN ('finished', 'failed');
