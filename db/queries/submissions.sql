-- name: InsertSubmission :exec
INSERT INTO submissions
    (id, status, job, result, started_at, attempt_count, lease_expires_at, failure_message)
VALUES
    (sqlc.arg(id), sqlc.arg(status), sqlc.arg(job), sqlc.narg(result),
     sqlc.narg(started_at), sqlc.arg(attempt_count), sqlc.narg(lease_expires_at),
     sqlc.narg(failure_message));

-- name: GetSubmission :one
SELECT id, status, job, result, created_at, started_at, attempt_count,
       lease_expires_at, failure_message
FROM submissions
WHERE id = $1;

-- name: UpdateSubmission :execrows
UPDATE submissions
SET status = sqlc.arg(new_status),
    result = sqlc.narg(result),
    started_at = sqlc.narg(started_at),
    lease_expires_at = CASE
        WHEN sqlc.arg(new_status) IN ('finished', 'failed') THEN NULL
        WHEN sqlc.arg(new_status) = 'running' THEN lease_expires_at
        ELSE sqlc.narg(lease_expires_at)
    END,
    failure_message = sqlc.narg(failure_message),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_count)
  AND (
      (status = 'running' AND lease_expires_at > NOW())
      OR (status = 'queued' AND sqlc.arg(new_status) = 'failed' AND attempt_count = 0)
  );

-- name: AcquireSubmissionAdmissionLock :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key));

-- name: CountQueuedSubmissions :one
SELECT COUNT(*)::bigint
FROM submissions
WHERE status = sqlc.arg(status);

-- name: CountSubmissionsByStatus :one
SELECT
    COUNT(*) FILTER (WHERE status = sqlc.arg(queued_status))::bigint AS queued,
    COUNT(*) FILTER (WHERE status = sqlc.arg(running_status))::bigint AS running,
    COUNT(*) FILTER (WHERE status = sqlc.arg(failed_status))::bigint AS failed
FROM submissions;

-- name: RenewSubmissionLease :execrows
UPDATE submissions
SET lease_expires_at = GREATEST(
        lease_expires_at,
        NOW() + make_interval(secs => sqlc.arg(lease_seconds)::float8)
    ),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND status = sqlc.arg(status)
  AND attempt_count = sqlc.arg(attempt_count)
  AND lease_expires_at > NOW();

-- name: LockExpiredSubmissions :many
SELECT id, attempt_count
FROM submissions
WHERE status = sqlc.arg(status)
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= NOW()
ORDER BY lease_expires_at, created_at
FOR UPDATE SKIP LOCKED;

-- name: RequeueExpiredSubmission :execrows
UPDATE submissions
SET status = sqlc.arg(status),
    result = NULL,
    started_at = NULL,
    lease_expires_at = NULL,
    failure_message = sqlc.arg(failure_message),
    updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: FailExpiredSubmission :execrows
UPDATE submissions
SET status = sqlc.arg(status),
    result = sqlc.arg(result),
    lease_expires_at = NULL,
    failure_message = sqlc.arg(failure_message),
    updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: ClaimNextSubmission :one
UPDATE submissions
SET status = sqlc.arg(running_status),
    attempt_count = attempt_count + 1,
    started_at = NOW(),
    lease_expires_at = NOW() + make_interval(secs => sqlc.arg(lease_seconds)::float8),
    updated_at = NOW()
WHERE id = (
    SELECT queued.id
    FROM submissions AS queued
    WHERE queued.status = sqlc.arg(queued_status)
    ORDER BY queued.created_at
    FOR UPDATE OF queued SKIP LOCKED
    LIMIT 1
)
RETURNING submissions.id, submissions.attempt_count;
