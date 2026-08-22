# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes.

## Current Status

The current API is asynchronous:

- `POST /submissions` creates a submission and returns a submission ID.
- `GET /submissions/{id}` fetches the current submission status and final result.
- `POST /submit` creates a submission and waits up to 10 seconds for the final result. If it times out, it returns `202` with the submission ID and `Location` header.
- `POST /run` executes code once with raw stdin and returns its output without checking test cases or creating a stored submission.
- `GET /health` checks whether the server is running.

Compatibility route:

- `POST /judge` is still available as an alias.

The server uses Postgres for durable submission storage and queue claiming. `DATABASE_URL` is required at startup.

## Requirements

Install these before running the project:

- Go
- Docker
- Postgres
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

## Postgres Setup

Postgres is required because submissions and results are stored durably and workers claim jobs through the database.

Create the database if it does not exist yet:

```bash
createdb -U postgres yexjudge
```

Create the submissions table:

```bash
psql -U postgres -d yexjudge -f db/submissions.sql
```

The schema stores the original job payload, current status, final result, and timestamps. The same table is also used as the queue: workers claim queued submissions with `FOR UPDATE SKIP LOCKED`.

If you prefer running Postgres through Docker:

```bash
docker run --name yexjudge-postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=yexjudge \
  -p 5432:5432 \
  -d postgres:16
```

Then create the table:

```bash
psql 'postgres://postgres:postgres@localhost:5432/yexjudge?sslmode=disable' -f db/submissions.sql
```

For an existing database, apply queue lease columns and the recovery backfill:

```bash
psql 'postgres://postgres@localhost:5432/yexjudge?sslmode=disable' -f db/migrations/002_queue_leases.sql
```

## Run the Server

From the project root, using Postgres:

```bash
DATABASE_URL='postgres://postgres@localhost:5432/yexjudge?sslmode=disable' go run ./cmd/server
```

If your Postgres user uses a password, include it in the URL:

```bash
DATABASE_URL='postgres://postgres:postgres@localhost:5432/yexjudge?sslmode=disable' go run ./cmd/server
```

Optional environment variables:

```bash
PORT=8080
WORKER_COUNT=4
SANDBOX_POOL_SIZE=4
DATABASE_URL=postgres://postgres@localhost:5432/yexjudge?sslmode=disable
QUEUE_POLL_INTERVAL_MS=500
QUEUE_LEASE_MS=60000
QUEUE_RECOVERY_INTERVAL_MS=1000
QUEUE_MAX_ATTEMPTS=3
SUBMIT_TIMEOUT_MS=10000
RUN_CONCURRENCY=2
```

Phase 3 execution limits and API behavior:

- JSON request bodies are limited to 1 MiB and use strict decoding: unknown fields and trailing JSON values are rejected with `400`.
- Every response includes an `X-Request-ID`; a valid client-supplied ID is preserved. API errors use `{ "error": { "code", "message", "requestId" } }`.
- `/run` has independent admission controlled by `RUN_CONCURRENCY` (default `2`). A full direct-run budget returns `429`; queued `/submissions` are not counted against it.
- Compiler and runtime stdout/stderr are each capped at 64 KiB. Exceeding the cap cancels execution and produces `output_limit_exceeded`.
- Docker compile, start, update, exec, restart, and staging failures are infrastructure failures. Terminal async jobs persist `infrastructure_error` with a capped diagnostic rather than silently losing the result.
- Compile containers run without network access, with bounded CPU, memory, PIDs, filesystem access, and an unprivileged user. Runtime sandboxes use the shared `yexjudge-runtime:latest` image with the same restrictions.
- Reusable sandboxes are readiness-checked after startup and restart. An execution-triggered restart is not reset a second time by pool release; failed reset/readiness checks replace that sandbox.

Authentication and per-user rate limiting are deferred because the current service has no user/account model. Recovery of submissions left in `running` after a process crash is Phase 4.

Expected log:

```text
YexJudge server running on :8080
```

When Postgres is enabled, the server also logs:

```text
using Postgres store and queue
```

At startup, the server creates reusable Docker runtime sandboxes. If startup fails, check that `yexjudge-runtime:latest` exists and Docker is running.

## Health Check

In another terminal:

```bash
curl http://localhost:8080/health
```

## Run Code

To execute code once without test cases, use `POST /run`:

```bash
curl -X POST http://localhost:8080/run \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "sourceCode": "a, b = 4, 7\nprint(a + b)",
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Example response:

```json
{
  "status": "accepted",
  "output": "11",
  "runtimeMs": 4
}
```

`/run` currently supports source code that does not require external input. Initialize values inside the submitted program. Use `/submit` for LeetCode-style function submissions.

Direct `/run` requests are admitted separately from queued submissions. When the configured `RUN_CONCURRENCY` capacity is full, the endpoint returns `429`. Program and compiler stdout/stderr are capped; exceeding the cap returns `output_limit_exceeded`.

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

For a single-request workflow, use `POST /submit` with the same JSON payload. A completed judge result is returned directly. The wait limit can be changed with `SUBMIT_TIMEOUT_MS`; after the limit, use `GET /submissions/{id}` with the returned ID.

## Submit Function-Style Code

YexJudge also supports a LeetCode-style C++ function mode. In this mode, the user submits a `class Solution`, and the request includes function metadata so the judge can generate a hidden driver.

Submit a C++ `twoSum` solution:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "class Solution {\npublic:\n    vector<int> twoSum(vector<int>& nums, int target) {\n        unordered_map<int, int> seen;\n        for (int i = 0; i < nums.size(); i++) {\n            int need = target - nums[i];\n            if (seen.count(need)) return {seen[need], i};\n            seen[nums[i]] = i;\n        }\n        return {};\n    }\n};",
    "function": {
      "name": "twoSum",
      "returnType": "vector<int>",
      "params": [
        {
          "name": "nums",
          "type": "vector<int>&"
        },
        {
          "name": "target",
          "type": "int"
        }
      ]
    },
    "testCases": [
      {
        "id": 1,
        "args": [[2, 7, 11, 15], 9],
        "expected": [0, 1]
      },
      {
        "id": 2,
        "args": [[3, 2, 4], 6],
        "expected": [1, 2]
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Function mode currently supports C++ and these value types:

- `int`
- `long long`
- `double`
- `bool`
- `string`
- `vector<int>`
- `vector<long long>`
- `vector<double>`
- `vector<bool>`
- `vector<string>`
- `ListNode*`
- `TreeNode*`
- `RandomListNode*`
- `Node*` for random-pointer lists
- `GraphNode*`
- `optional<T>` values such as `optional<int>`
- recursive compositions such as `vector<vector<int>>` and `optional<vector<int>>`

Reference parameters such as `vector<int>&` and `const vector<int>&` are accepted in metadata. The generated driver stores them as normal values and passes them into the submitted function.

For identity-sensitive functions, metadata can declare generic `disjoint` or `same_as` postconditions. The result contains boolean postcondition observations; raw memory addresses are never exposed.

## Submit Class-Style Code

C++ Class Mode supports a generic constructor followed by a sequence of declared operations. It does not contain drivers for individual design problems.

A testcase uses `constructorArgs`, `operations`, and one expected result per operation:

```json
{
  "mode": "class",
  "class": {
    "name": "Counter",
    "constructor": { "params": [{ "name": "initial", "type": "int" }] },
    "operations": [
      { "name": "add", "returnType": "void", "params": [{ "name": "amount", "type": "int" }] },
      { "name": "get", "returnType": "int", "params": [] }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "constructorArgs": [3],
      "operations": [
        { "name": "add", "args": [4] },
        { "name": "get", "args": [] }
      ],
      "expected": [null, 7]
    }
  ]
}
```

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

Workers claim submissions with a lease. The claim increments `attemptCount`; heartbeats and final updates are fenced to that attempt. Expired leases are requeued until `QUEUE_MAX_ATTEMPTS` is reached, then stored as `failed` with an `infrastructure_error` result. Lease recovery runs at startup and periodically, so a crashed worker does not strand a submission in `running`.

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
- `output_limit_exceeded`
- `infrastructure_error`

## Verify Postgres Persistence

After submitting a job, check the latest rows:

```bash
psql -U postgres -d yexjudge -c "SELECT id, status, created_at, updated_at FROM submissions ORDER BY created_at DESC LIMIT 5;"
```

To inspect the stored verdict:

```bash
psql -U postgres -d yexjudge -c "SELECT id, status, result FROM submissions ORDER BY created_at DESC LIMIT 1;"
```

## Supported Languages

Use these values in the `language` field:

- `c`
- `cpp`
- `python`
- `go` (legacy stdin/stdout support; no Function Mode backend planned)
- `java`

LeetCode-style Function Mode currently targets C++ only. Python and Java Function/Class Mode support are deferred until the C++ roadmap is complete. The eventual status of the existing Go option will be decided separately; no new Go feature work is planned.

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

Run the local Go checks:

```bash
GOCACHE=/tmp/yexjudge-go-cache go test ./...
GOCACHE=/tmp/yexjudge-go-cache go test -race ./...
GOCACHE=/tmp/yexjudge-go-cache go vet ./...
```

Format Go code:

```bash
gofmt -w cmd internal
```

Run the opt-in Postgres integration tests. The database must already have the `submissions` table:

```bash
YEXJUDGE_TEST_DATABASE_URL='postgres://postgres@localhost:5432/yexjudge?sslmode=disable' \
  GOCACHE=/tmp/yexjudge-go-cache \
  go test ./internal/judge -count=1
```

Run the opt-in API/Docker integration test as well. It builds a temporary server binary, starts it against Postgres and the local Docker images, and verifies `/submissions`, `/submit`, `/run`, and compilation errors:

```bash
YEXJUDGE_TEST_DATABASE_URL='postgres://postgres@localhost:5432/yexjudge?sslmode=disable' \
  GOCACHE=/tmp/yexjudge-go-cache \
  go test ./cmd/server -run TestAPIIntegration -count=3 -v
```

The integration tests are skipped when `YEXJUDGE_TEST_DATABASE_URL` is not set, so `go test ./...` remains self-contained.
The API test uses one worker and one runtime sandbox to exercise the supported single-sandbox configuration. Its test-only synchronous wait budget is 4 seconds, below the 5-second HTTP client timeout. Cold C/C++, Go, and Java compiler images/build caches can make the first submission take tens of seconds; the asynchronous polling budget allows that startup cost.

## Troubleshooting

If the server fails during startup:

- make sure Docker is running
- make sure `yexjudge-runtime:latest` was built
- run `docker images | grep yexjudge-runtime`
- if using Postgres, make sure `DATABASE_URL` is correct
- if using Postgres, make sure `db/submissions.sql` has been applied

If the first C, C++, Go, or Java submission is slow:

- Docker may be pulling the compile image for the first time
- pre-pull the compile images listed above
- a cold compiler/container setup is expected to take tens of seconds; the API acceptance test allows for it

If submissions stay queued:

- check the server logs
- make sure workers started successfully
- make sure reusable sandbox containers were created at startup
- if using Postgres, check that workers can update rows in the `submissions` table

If execution fails with missing commands:

- rebuild the runtime image
- confirm the runtime image includes `python3` and Java runtime support

## Next Major Work

The implementation order is maintained in [`plan.md`](plan.md). After the test baseline is complete, the next major runtime work is hardening execution/admission control followed by recoverable queue leases and worker recovery.
