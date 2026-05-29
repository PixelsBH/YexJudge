# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes.

## Current Status

The current API is asynchronous:

- `POST /submissions` creates a submission and returns a submission ID.
- `GET /submissions/{id}` fetches the current submission status and final result.
- `GET /health` checks whether the server is running.

Compatibility route:

- `POST /judge` is still available as an alias.

The server currently uses an in-memory submission store and queue, so submissions are lost when the server restarts.

## Requirements

Install these before running the project:

- Go
- Docker
- `curl`, for testing the API from the terminal

Docker must be running, and the user running the server must be allowed to execute Docker commands.

Check Docker access:

```bash
docker ps
```

If this fails with a permission error, fix Docker permissions first or run the server in an environment where Docker is available.

## Docker Images

YexJudge uses one reusable runtime sandbox image for executing C, C++, Go, Python, and Java programs.

Build the runtime image from the project root:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

Compiled languages still use separate compile images:

- C and C++: `gcc:13`
- Go: `golang:1.24-alpine`
- Java: `eclipse-temurin:17-jdk`

Optional, but recommended, pull them once before testing:

```bash
docker pull gcc:13
docker pull golang:1.24-alpine
docker pull eclipse-temurin:17-jdk
```

Python does not need a separate compile image.

## Run the Server

From the project root:

```bash
go run ./cmd/server
```

Optional environment variables:

```bash
PORT=8080
WORKER_COUNT=4
QUEUE_SIZE=100
SANDBOX_POOL_SIZE=4
```

Expected log:

```text
YexJudge server running on :8080
```

At startup, the server creates reusable Docker runtime sandboxes. If startup fails, check that `yexjudge-runtime:latest` exists and Docker is running.

## Health Check

In another terminal:

```bash
curl http://localhost:8080/health
```

## Submit Code

Submit a Python solution:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "sourceCode": "a, b = map(int, input().split())\nprint(a + b)",
    "testCases": [
      {
        "id": 1,
        "input": "2 3",
        "expectedOutput": "5"
      },
      {
        "id": 2,
        "input": "10 20",
        "expectedOutput": "30"
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Example response:

```json
{
  "submissionId": "1780000000000000000",
  "status": "queued"
}
```

Copy the returned `submissionId`.

## Fetch Result

Use the submission ID returned by `POST /submissions`:

```bash
curl http://localhost:8080/submissions/1780000000000000000
```

While the submission is being processed, the status can be:

- `queued`
- `running`

After processing, the status should become:

- `finished`
- `failed`

Example accepted response:

```json
{
  "id": "1780000000000000000",
  "status": "finished",
  "result": {
    "status": "accepted",
    "runtimeMs": 12
  }
}
```

Possible result statuses include:

- `accepted`
- `wrong_answer`
- `time_limit_exceeded`
- `runtime_error`
- `compilation_error`
- `memory_limit_exceeded`
- `validation_error`

## Supported Languages

Use these values in the `language` field:

- `c`
- `cpp`
- `python`
- `go`
- `java`

## C++ Example

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "#include <bits/stdc++.h>\nusing namespace std;\nint main(){ long long a,b; cin >> a >> b; cout << a + b << \"\\n\"; }",
    "testCases": [
      {
        "id": 1,
        "input": "4 7",
        "expectedOutput": "11"
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

## Java Example

Java submissions must define a `Main` class:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "java",
    "sourceCode": "import java.util.*; public class Main { public static void main(String[] args) { Scanner sc = new Scanner(System.in); int a = sc.nextInt(); int b = sc.nextInt(); System.out.println(a + b); } }",
    "testCases": [
      {
        "id": 1,
        "input": "8 9",
        "expectedOutput": "17"
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

## Development Checks

Run Go tests:

```bash
go test ./...
```

Format Go code:

```bash
gofmt -w cmd internal
```

## Troubleshooting

If the server fails during startup:

- make sure Docker is running
- make sure `yexjudge-runtime:latest` was built
- run `docker images | grep yexjudge-runtime`

If the first C, C++, Go, or Java submission is slow:

- Docker may be pulling the compile image for the first time
- pre-pull the compile images listed above

If submissions stay queued:

- check the server logs
- make sure workers started successfully
- make sure reusable sandbox containers were created at startup

If execution fails with missing commands:

- rebuild the runtime image
- confirm the runtime image includes `python3` and Java runtime support

## Next Major Work

The next architectural step is to replace the in-memory store and queue with Postgres-backed durable submission storage and job claiming. See [`architecture.md`](architecture.md) for the full work order.
