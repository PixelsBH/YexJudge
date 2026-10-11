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

## Load testing and profiling

With the server running, submit a burst of asynchronous C++ jobs and measure API admission latency:

```bash
bash scripts/load-test.sh 10 4
```

Set `YEXJUDGE_BASE_URL` to target another server:

```bash
YEXJUDGE_BASE_URL=http://localhost:8080 bash scripts/load-test.sh 20 4
```

The script requires Bash and `curl`; it reports accepted/failed requests and average admission time. It measures queue admission, not full completion latency.

The Go server includes an opt-in `net/http/pprof` listener for CPU and memory profiling. It is disabled unless `PPROF_ADDR` is set. For a direct local run, bind it to loopback:

```bash
PPROF_ADDR=127.0.0.1:6060 go run ./cmd/server
```

For Compose, first complete the [Compose setup](../README.md#run-with-docker-compose), then set this in the YexJudge `.env` file; Compose publishes the profiling port only on host loopback:

```dotenv
PPROF_ADDR=0.0.0.0:6060
```

Start or recreate the service so it picks up the setting, then wait until it is ready:

```bash
docker compose up --build -d
curl --fail http://127.0.0.1:8080/ready
```

In one terminal, start a 30-second CPU profile and open Go's pprof UI (including the flame graph view):

```bash
go tool pprof -http=127.0.0.1:6061 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
```

While it is collecting, run representative traffic in another terminal, adjusting the workload as needed:

```bash
bash scripts/load-test.sh 20 4
```

The profiling endpoint is unauthenticated and exposes runtime details; keep it on loopback or a trusted private network and disable it after profiling. A Go CPU profile shows CPU used by the YexJudge server process, not CPU used by compiler/runtime containers or time waiting on PostgreSQL/Docker. Use completion latency, queue-wait metrics, and container/database resource metrics alongside pprof to identify end-to-end bottlenecks.
