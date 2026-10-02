# Operations

## Requirements and runtime images

YexJudge requires Go, Docker, and PostgreSQL. Docker must be running, and the server process must be allowed to execute Docker commands. Verify Docker access with:

```bash
docker ps
```

YexJudge uses one reusable runtime sandbox image for C, C++, Go, Python, and Java. Build it from the project root:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

Compiled languages also use separate compiler images:

- C and C++: `gcc:13`
- Go: `golang:1.24-alpine`
- Java: `eclipse-temurin:17-jdk`

Pre-pulling them is optional, but avoids waiting on the first submission:

```bash
docker pull gcc:13
docker pull golang:1.24-alpine
docker pull eclipse-temurin:17-jdk
```

Python does not require a separate compile image.

## PostgreSQL

PostgreSQL stores submissions and results and is also used for queue claiming. Create the database if it does not exist:

```bash
createdb -U postgres yexjudge
```

The server applies `db/submissions.sql` and numbered files under `db/migrations/` automatically during startup. To create the schema manually instead:

```bash
psql -U postgres -d yexjudge -f db/submissions.sql
```

To run PostgreSQL in Docker:

```bash
docker run --name yexjudge-postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=yexjudge \
  -p 5432:5432 \
  -d postgres:16
```

The corresponding local connection string is:

```text
postgres://postgres:postgres@localhost:5432/yexjudge?sslmode=disable
```

For an existing database that predates queue leases, the server migration handles the update automatically. It can also be applied manually with `psql` if needed:

```bash
psql 'postgres://postgres@localhost:5432/yexjudge?sslmode=disable' -f db/migrations/002_queue_leases.sql
```

## Server configuration

Copy the safe template from the project root and set `DATABASE_URL` in `.env`:

```bash
cp .env.example .env
go run ./cmd/server
```

The real `.env` file is ignored by Git. Do not commit passwords or secrets. Explicit process environment variables take precedence over `.env`, so a one-off override works too:

```bash
DATABASE_URL='postgres://postgres:postgres@localhost:5432/yexjudge?sslmode=disable' go run ./cmd/server
```

Available configuration variables include:

```text
PORT=8080
WORKER_COUNT=4
SANDBOX_POOL_SIZE=4
COMPILE_SLOTS=2
DATABASE_URL=postgres://postgres@localhost:5432/yexjudge?sslmode=disable
QUEUE_POLL_INTERVAL_MS=500
QUEUE_LEASE_MS=60000
QUEUE_RECOVERY_INTERVAL_MS=1000
QUEUE_MAX_ATTEMPTS=3
SUBMIT_TIMEOUT_MS=10000
```

Capacity and lifecycle details:

- `WORKER_COUNT` bounds concurrent queue orchestration (default `4`, allowed `1-64`).
- `SANDBOX_POOL_SIZE` bounds concurrent runtime execution (default `4`, allowed `1-64`).
- `COMPILE_SLOTS` controls the separate compile-worker pool and concurrent disposable Docker compiler containers (default `2`, allowed `1-16`). Interpreted submissions do not consume compile workers.
- Startup rejects values outside those ranges and capacity configurations reserving more than 8 GiB. The default reservation is 3 GiB (`4 x 512 MiB` runtime sandboxes plus `2 x 512 MiB` compile slots).
- During graceful shutdown, readiness becomes unavailable, new requests stop, active/queued work is cancelled within the shutdown deadline, workers are awaited, and pool-owned sandbox containers are removed.

Execution and API safeguards:

- JSON bodies are limited to 1 MiB; unknown fields and trailing JSON values are rejected with `400`.
- Every response includes an `X-Request-ID`; valid client-supplied IDs are preserved. API errors use `{ "error": { "code", "message", "requestId" } }`.
- Compiler and runtime stdout/stderr are each capped at 64 KiB. Exceeding the cap cancels execution and returns `output_limit_exceeded`.
- Compile containers run without network access and with bounded CPU, memory, PIDs, filesystem access, and an unprivileged user. Runtime sandboxes use the shared image with similar restrictions.
- Reusable sandboxes are checked after startup and restart. Failed reset or readiness checks cause replacement.
- Authentication and per-user rate limiting are deferred because the service does not yet have a user/account model.

## Health and diagnostics

`/health` is a liveness check and confirms only that the process is running:

```bash
curl http://localhost:8080/health
```

`/ready` checks that PostgreSQL is reachable, the runtime sandbox pool has initialized capacity, and the server is not shutting down:

```bash
curl -i http://localhost:8080/ready
```

It returns `200` with `{"status":"ready"}` when work can be accepted, or `503` with dependency details otherwise.

For operational visibility, query:

```bash
curl http://localhost:8080/diagnostics
```

The response includes queued/running/failed counts, worker and compile-worker capacity, runtime sandbox utilization, and cumulative latency histograms for queue wait, compilation, sandbox acquisition, staging, execution, and sandbox reset. Structured JSON logs include submission ID, language, worker ID, attempt, status transitions, and stage timings. `/diagnostics` exposes aggregate state only; it does not return source code or submission results.

## Docker Compose deployment

For a local all-in-one deployment, run from the project root:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
mkdir -p .yexjudge-workspaces
docker compose up --build
```

Compose publishes PostgreSQL on host port `5433` by default to avoid conflicts with a host database on `5432`. Change it with `POSTGRES_HOST_PORT`, for example:

```bash
POSTGRES_HOST_PORT=55432 docker compose up --build
```

The application container connects to PostgreSQL internally at `postgres:5432`. It mounts the Docker socket because YexJudge launches isolated sibling compile/runtime containers. This is intended for trusted local development, not an untrusted multi-tenant deployment. The project directory is mounted at the same absolute path inside the app container so the host Docker daemon can resolve workspace bind mounts.

Stop the deployment with:

```bash
docker compose down
```

PostgreSQL data remains in the `yexjudge-postgres-data` named volume unless it is explicitly removed.

## Troubleshooting

If the server fails during startup:

- Confirm Docker is running and accessible to the server process.
- Confirm `yexjudge-runtime:latest` exists: `docker images | grep yexjudge-runtime`.
- Check that `DATABASE_URL` in `.env` is correct, or provide it explicitly in the environment.
- Check startup logs for migration errors; schema and numbered migrations are applied automatically.

If Docker Compose is not recognized or reports `unknown flag: --build`:

- Install the Docker Compose v2 plugin and verify with `docker compose version`.
- If only the legacy command is installed, use `docker-compose up --build`.
- Run the command from the project root, where `docker-compose.yml` is located.
- On Debian/Ubuntu, install with `sudo apt-get install docker-compose-plugin`.

If the first C, C++, Go, or Java submission is slow, Docker may be downloading the compiler image. Pre-pull the images listed above; a cold compiler/container setup can take tens of seconds.

If submissions remain queued, check server logs, worker startup, runtime sandbox creation, and (when using PostgreSQL) whether workers can update rows in the `submissions` table.

If execution fails with missing commands, rebuild the runtime image and confirm it contains `python3` and Java runtime support.

To inspect recent persisted submissions or a stored verdict:

```bash
psql -U postgres -d yexjudge -c "SELECT id, status, created_at, updated_at FROM submissions ORDER BY created_at DESC LIMIT 5;"
psql -U postgres -d yexjudge -c "SELECT id, status, result FROM submissions ORDER BY created_at DESC LIMIT 1;"
```
