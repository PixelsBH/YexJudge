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

These integration tests are skipped when `YEXJUDGE_TEST_DATABASE_URL` is unset, so `go test ./...` remains self-contained. The API test uses one worker and one runtime sandbox. Cold C/C++, Go, and Java compiler images/build caches can make the first submission take tens of seconds.

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
