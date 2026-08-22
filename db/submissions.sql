CREATE TABLE IF NOT EXISTS submissions (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    job JSONB NOT NULL,
    result JSONB,
    started_at TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TIMESTAMPTZ,
    failure_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS submissions_status_created_at_idx
    ON submissions (status, created_at);

CREATE INDEX IF NOT EXISTS submissions_running_lease_idx
    ON submissions (lease_expires_at)
    WHERE status = 'running';
