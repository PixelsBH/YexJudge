# Development

## Go checks

Run the unit and static-analysis checks from the project root:

```bash
GOCACHE=/tmp/yexjudge-go-cache go test ./...
GOCACHE=/tmp/yexjudge-go-cache go test -race ./...
GOCACHE=/tmp/yexjudge-go-cache go vet ./...
```

Format Go code with:

```bash
gofmt -w cmd internal
```

## PostgreSQL query generation

The SQL source of truth is in `db/queries`, and sqlc-generated pgx queries live in `internal/dbquery`. Do not edit generated files directly. Install the pinned generator and regenerate after changing query sources or schema migrations:

```bash
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
sqlc generate -f sqlc.yaml
```

## Integration tests

PostgreSQL integration tests are opt-in. The database must be available; migrations/schema are normally applied by server startup. Set `YEXJUDGE_TEST_DATABASE_URL` to enable the tests:

```bash
YEXJUDGE_TEST_DATABASE_URL='postgres://postgres@localhost:5432/yexjudge?sslmode=disable' \
  GOCACHE=/tmp/yexjudge-go-cache \
  go test ./internal/judge -count=1
```

The API/Docker integration test also requires PostgreSQL, Docker, and the runtime/compiler images. It starts a temporary server and exercises submission endpoints, diagnostics, and compilation errors:

```bash
YEXJUDGE_TEST_DATABASE_URL='postgres://postgres@localhost:5432/yexjudge?sslmode=disable' \
  GOCACHE=/tmp/yexjudge-go-cache \
  go test ./cmd/server -run TestAPIIntegration -count=3 -v
```

These integration tests are skipped when `YEXJUDGE_TEST_DATABASE_URL` is unset, so `go test ./...` remains self-contained. The API test uses one worker and one runtime sandbox. Cold C/C++ compiler images/build caches can make the first compiled submission take tens of seconds; Python submissions run directly in the runtime image.

## Load testing

With the server running, submit a burst of asynchronous C++ jobs and measure API admission latency:

```bash
bash scripts/load-test.sh 10 4
```

Set `YEXJUDGE_BASE_URL` to target another server:

```bash
YEXJUDGE_BASE_URL=http://localhost:8080 bash scripts/load-test.sh 20 4
```

The script requires Bash and `curl`; it reports accepted/failed requests and average admission time. It measures queue admission, not full completion latency.

The runtime supervisor executes all cases for each submission through one `docker exec`, starting a fresh UID/GID 10001 contestant process per case. It sends each case input only to that process's stdin and cleans writable case filesystems and UID 10001 processes between cases. Reported per-case runtime is user plus system CPU time from `wait4`; a wall-clock timeout remains as a backstop for sleeping or blocked programs. This changes runtime semantics from Docker-client wall time to contestant CPU time. The design has no per-case cgroup, so its cleanup and process-tree resource accounting have limits; run the Docker adversarial tests on each target Docker/kernel configuration and review the containment boundary before treating it as a security boundary.

The C++ compiler image is built by Compose as `yexjudge-cpp-compile:latest`. It precompiles `bits/stdc++.h`; the shared flags are in `internal/judge/languages/cpp_compile_flags.txt`, used by both the Go compile command and image build. The image build fails if the precompiled header self-check does not show GCC consuming the `.gch` file.
