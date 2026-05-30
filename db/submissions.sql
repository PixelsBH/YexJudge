CREATE TABLE IF NOT EXISTS submissions (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    job JSONB NOT NULL,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS submissions_status_created_at_idx
    ON submissions (status, created_at);
