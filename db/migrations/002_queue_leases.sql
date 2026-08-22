ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS failure_message TEXT;

CREATE INDEX IF NOT EXISTS submissions_running_lease_idx
    ON submissions (lease_expires_at)
    WHERE status = 'running';

-- Rows created before leases existed are treated as their first attempt. Use
-- updated_at as the best available start time and let normal recovery decide
-- whether the old worker lease has expired.
UPDATE submissions
SET attempt_count = GREATEST(attempt_count, 1),
    started_at = COALESCE(started_at, updated_at),
    lease_expires_at = COALESCE(lease_expires_at, updated_at + INTERVAL '60 seconds')
WHERE status = 'running';
