# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes.

## Current Status

The current API is asynchronous:

- `POST /submissions` creates a submission and returns a submission ID.
- `GET /submissions/{id}` fetches the current submission status and final result.
- `POST /submit` creates a submission and waits up to 10 seconds for the final result. If it times out, it returns `202` with the submission ID and `Location` header.
- `GET /health` checks whether the server is running.
- `GET /diagnostics` returns local operational counts, worker/sandbox utilization, and bounded latency histograms.

Compatibility route:

- `POST /judge` is still available as an alias.

The server uses Postgres for durable submission storage and queue claiming. The optional project-root `.env` file is loaded at startup for local development; explicitly exported environment variables take precedence. `DATABASE_URL` is required after configuration is loaded.

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

The schema stores the original job payload, current status, final result, and timestamps. The same table is also used as the queue: workers claim queued submissions with `FOR UPDATE SKIP LOCKED`. The server applies `db/submissions.sql` and numbered files under `db/migrations/` automatically during startup, so manual `psql -f` commands are optional when starting a new local instance.

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

From the project root, create a local environment file from the safe template:

```bash
cp .env.example .env
```

Edit `.env` if your Postgres credentials or local capacity differ, then start the server without repeating the database URL:

```bash
go run ./cmd/server
```

The `.env` file is ignored by Git. Do not commit passwords or other secrets. Explicit environment variables still override `.env`, so a one-off override remains possible:

```bash
DATABASE_URL='postgres://postgres:postgres@localhost:5432/yexjudge?sslmode=disable' go run ./cmd/server
```

Optional environment variables:

```bash
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

Capacity controls and graceful shutdown:

- `WORKER_COUNT` bounds concurrent queue orchestration (default `4`, allowed `1-64`).
- `SANDBOX_POOL_SIZE` bounds concurrent runtime execution (default `4`, allowed `1-64`).
- `COMPILE_SLOTS` controls the dedicated compile-worker pool and independently bounds concurrent disposable Docker compiler containers (default `2`, allowed `1-16`). Interpreted submissions do not consume compile workers.
- Startup rejects values outside those ranges or capacity combinations reserving more than 8 GiB at the configured worst-case limits. The default reservation is 3 GiB (`4 x 512 MiB` runtime sandboxes plus `2 x 512 MiB` compile slots).
- During graceful shutdown, the server marks readiness as unavailable, stops accepting requests, cancels queued/active work within the shutdown deadline, waits for compile and submission workers, and removes all warm and replacement sandbox containers.

Execution limits and API behavior:

- JSON request bodies are limited to 1 MiB and use strict decoding: unknown fields and trailing JSON values are rejected with `400`.
- Every response includes an `X-Request-ID`; a valid client-supplied ID is preserved. API errors use `{ "error": { "code", "message", "requestId" } }`.
- Compiler and runtime stdout/stderr are each capped at 64 KiB. Exceeding the cap cancels execution and produces `output_limit_exceeded`.
- Docker compile, start, update, exec, restart, and staging failures are infrastructure failures. Terminal async jobs persist `infrastructure_error` with a capped diagnostic rather than silently losing the result.
- Compile containers run without network access, with bounded CPU, memory, PIDs, filesystem access, and an unprivileged user. Runtime sandboxes use the shared `yexjudge-runtime:latest` image with the same restrictions.
- Reusable sandboxes are readiness-checked after startup and restart. An execution-triggered restart is not reset a second time by pool release; failed reset/readiness checks replace that sandbox.

Authentication and per-user rate limiting are deferred because the current service has no user/account model. Submissions left in `running` after a process crash are recovered through bounded leases and retries.

Expected log:

```text
YexJudge server running on :8080
```

When Postgres is enabled, the server also logs:

```text
using Postgres store and queue
```

At startup, the server creates reusable Docker runtime sandboxes. If startup fails, check that `yexjudge-runtime:latest` exists and Docker is running.

## Health and Readiness Checks

`/health` is a liveness check and only confirms that the process is running:

```bash
curl http://localhost:8080/health
```

`/ready` is a readiness check. It verifies that Postgres is reachable, the runtime sandbox pool has initialized capacity, and the server is not shutting down:

```bash
curl -i http://localhost:8080/ready
```

It returns `200` with `{"status":"ready"}` when the service can accept work and `503` with dependency details when it cannot.

## Diagnostics

For local operational visibility:

```bash
curl http://localhost:8080/diagnostics
```

The response includes queued/running/failed submission counts, worker busy/total capacity, compile-worker capacity, available/busy/starting runtime sandboxes, and cumulative latency histograms for queue wait, compilation, sandbox acquisition, staging, testcase/runtime execution, and sandbox reset. Logs are JSON structured events containing submission ID, language, worker ID, attempt, status transitions, and execution-stage durations.

The endpoint exposes aggregate state only; it does not return source code or submission results. Authentication for deployments with users remains future work.

## Submit Code

The main documented submission path is C++. Conventional stdin/stdout submissions are supported for C, Python, Go, and Java as well, but the metadata-driven LeetCode-style Function/Class system currently supports C++ only.

For a combined workflow, use `POST /submit` with the submission payload shown in the C++ example below. A completed judge result is returned directly. If processing exceeds `SUBMIT_TIMEOUT_MS`, the endpoint returns `202` with a submission ID and `Location`; poll `GET /submissions/{id}` only in that case.

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

### Tree Function Example: Maximum Path Sum

Function Mode also supports registered runtime types such as `TreeNode*`. The judge constructs the tree from the JSON argument and compares the scalar result returned by the submitted recursive method.

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "class Solution {\npublic:\n    int maxPathSum(TreeNode* root) {\n        int maxSum = INT_MIN;\n        pathSum(root, maxSum);\n        return maxSum;\n    }\n    int pathSum(TreeNode* root, int& maxSum) {\n        if (!root) return 0;\n        int left = max(0, pathSum(root->left, maxSum));\n        int right = max(0, pathSum(root->right, maxSum));\n        maxSum = max(maxSum, left + right + root->val);\n        return root->val + max(left, right);\n    }\n};",
    "function": {
      "name": "maxPathSum",
      "returnType": "int",
      "params": [
        { "name": "root", "type": "TreeNode*" }
      ]
    },
    "testCases": [
      {
        "id": 1,
        "args": [[-10, 9, 20, null, null, 15, 7]],
        "expected": 42
      },
      {
        "id": 2,
        "args": [[-3]],
        "expected": -3
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

A testcase uses `constructorArgs`, `operations`, and one expected result per operation. This same metadata contract supports stateful designs such as Min Stack, Trie, HashMap, and LRU Cache. LRU Cache appears in the test suite only as a representative multi-case regression fixture; there is no LRU-specific production implementation.

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

### Complete LRU Cache submission example

The following submits a complete LRU Cache implementation through the generic Class Mode API. Start YexJudge first with `go run ./cmd/server` or Docker Compose, then run this command from any Bash shell:

```bash
curl -sS -X POST http://localhost:8080/submit \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'JSON'
{
  "language": "cpp",
  "mode": "class",
  "sourceCode": "class LRUCache {\n    int capacity;\n    list<pair<int, int>> items;\n    unordered_map<int, list<pair<int, int>>::iterator> index;\npublic:\n    LRUCache(int capacity) : capacity(capacity) {}\n\n    int get(int key) {\n        auto found = index.find(key);\n        if (found == index.end()) return -1;\n        items.splice(items.begin(), items, found->second);\n        return found->second->second;\n    }\n\n    void put(int key, int value) {\n        if (capacity <= 0) return;\n        auto found = index.find(key);\n        if (found != index.end()) {\n            found->second->second = value;\n            items.splice(items.begin(), items, found->second);\n            return;\n        }\n        items.emplace_front(key, value);\n        index[key] = items.begin();\n        if (static_cast<int>(index.size()) > capacity) {\n            auto leastRecent = prev(items.end());\n            index.erase(leastRecent->first);\n            items.pop_back();\n        }\n    }\n};",
  "class": {
    "name": "LRUCache",
    "constructor": {
      "params": [
        { "name": "capacity", "type": "int" }
      ]
    },
    "operations": [
      {
        "name": "put",
        "returnType": "void",
        "params": [
          { "name": "key", "type": "int" },
          { "name": "value", "type": "int" }
        ]
      },
      {
        "name": "get",
        "returnType": "int",
        "params": [
          { "name": "key", "type": "int" }
        ]
      }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "constructorArgs": [2],
      "operations": [
        { "name": "put", "args": [1, 1] },
        { "name": "put", "args": [2, 2] },
        { "name": "get", "args": [1] },
        { "name": "put", "args": [3, 3] },
        { "name": "get", "args": [2] },
        { "name": "put", "args": [4, 4] },
        { "name": "get", "args": [1] },
        { "name": "get", "args": [3] },
        { "name": "get", "args": [4] }
      ],
      "expected": [null, null, 1, null, -1, null, -1, 3, 4]
    }
  ],
  "limits": {
    "timeLimitMs": 1000,
    "memoryLimitMb": 128
  }
}
JSON
```

A successful response has a finished result with `"status": "accepted"`. If the bounded `/submit` wait expires first, it returns a submission ID instead; retrieve that submission using the endpoint described below.

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

The C++ implementation is the current project focus. C, Python, Go, and Java can run conventional stdin/stdout submissions, but their LeetCode-style Function/Class backends are future work and are not documented as fully supported yet.

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

## Python Example

Python is currently supported for conventional stdin/stdout submissions. It does not yet support the metadata-driven Function/Class system shown in the C++ examples.

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "sourceCode": "a, b = map(int, input().split())\nprint(a + b)",
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

## Docker Compose Deployment

For a local all-in-one deployment, run the commands from the project root. First build the runtime image and create the shared workspace directory:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
mkdir -p .yexjudge-workspaces
docker compose up --build
```

Compose publishes its PostgreSQL service on host port `5433` by default so it does not conflict with a PostgreSQL server already using host port `5432`. Change `POSTGRES_HOST_PORT` if you need another host port, for example `POSTGRES_HOST_PORT=55432 docker compose up --build`. The YexJudge container still connects to PostgreSQL internally through `postgres:5432`.

The Compose application container mounts the Docker socket because YexJudge launches isolated sibling compile/runtime containers. This setup is intended for trusted local development; do not expose the Docker socket to an untrusted multi-tenant service. The project directory is mounted at the same absolute path inside the application container so the host Docker daemon can resolve compile workspace bind mounts.

Stop the deployment with:

```bash
docker compose down
```

Postgres data is kept in the `yexjudge-postgres-data` named volume unless removed explicitly.

## Load Test

With the server running, submit a small burst of asynchronous C++ jobs and measure API admission latency:

```bash
bash scripts/load-test.sh 10 4
```

Use `YEXJUDGE_BASE_URL` to target another server, for example `YEXJUDGE_BASE_URL=http://localhost:8080 bash scripts/load-test.sh 20 4`. The script requires Bash and `curl`; it reports accepted/failed requests and average admission time. It measures queue admission, not full completion latency.

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

Run the opt-in API/Docker integration test as well. It builds a temporary server binary, starts it against Postgres and the local Docker images, and verifies `/submissions`, `/submit`, `/diagnostics`, and compilation errors:

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
- if using Postgres, make sure `DATABASE_URL` in `.env` is correct, or provide it explicitly in the environment
- if using Postgres, check the startup migration error; the server applies `db/submissions.sql` and numbered migrations automatically

If `docker compose` reports `unknown flag: --build` or is not recognized:

- install the Docker Compose v2 plugin for your distribution
- verify it with `docker compose version`
- if the legacy command is installed instead, use `docker-compose up --build`
- run the command from the project root, where `docker-compose.yml` is located

For example, on Debian/Ubuntu:

```bash
sudo apt-get install docker-compose-plugin
```

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

## Future Roadmap

The C++-first production milestone is complete. Optional future work is maintained in [`plan.md`](plan.md): adaptive capacity, additional Python/Java Function/Class backends, and an explicit decision about retaining or removing Go. These extensions are not required to use or showcase the current service.
