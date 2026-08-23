# YexJudge Development Plan

## Purpose

This is the working implementation order for YexJudge. It reflects the codebase as it exists today and is intentionally more actionable than `architecture.md`.

The order matters. Complete each phase's definition of done before treating the next phase as production-ready.

## Current Snapshot

Already implemented:

- asynchronous `POST /submissions` with `GET /submissions/{id}` result retrieval
- synchronous `POST /submit` convenience endpoint with a bounded wait
- Postgres-backed submission persistence and queue claiming with `FOR UPDATE SKIP LOCKED`
- fixed in-process worker pool
- fixed pool of reusable Docker runtime sandboxes
- one sandbox per submission and many test cases per borrowed sandbox
- C++, C, Python, Go, and Java language specs for the current stdin/stdout path
- C++ LeetCode-style Function Mode for scalar, recursive container, custom runtime, mutation, and identity cases
- generic C++ Class Mode for constructor/operation sequences
- verdicts for accepted, wrong answer, time limit exceeded, runtime error, compilation error, and validation error
- actual output included with a failed test case when output is available

Current architectural constraints:

- compile containers are created per compiled submission
- workers poll Postgres for queued jobs
- jobs left in `running` after a crash are not recovered
- opt-in Postgres store/queue integration tests exist behind `YEXJUDGE_TEST_DATABASE_URL`
- opt-in API/Docker integration tests cover the server lifecycle and core routes

## Current Delivery Target

The resume-ready milestone is a reliable C++-first production service, not complete multi-language LeetCode coverage.

Work toward this milestone in the following order:

1. Complete Phase 3: execution hardening and admission control.
2. Complete Phase 4: durable queue recovery and retry handling.
3. Complete Phase 5: minimum operational visibility needed to diagnose production behavior.
4. Complete Phase 6: bounded fixed capacity and runtime-pool lifecycle hardening.
5. Defer Phase 7: adaptive capacity and autoscaling.
6. Complete Phase 8: measured compile-latency improvements, only if Phase 5 shows they are needed.
7. Complete Phase 9: production packaging, deployment, health/readiness, and repeatable benchmarking.
8. Defer Phase 10: Python/Java Function/Class backends and the final Go keep/remove decision.

Phase 5 is the current plan's operational-visibility phase. It is not a reason to implement adaptive capacity; basic logs, timings, and diagnostics are required for a credible production project, while advanced scaling remains deferred.

## Phase 1: Design the Execution and Type Architecture

Status: **complete for the initial C++ scope**. Phase 1A–1D contracts, recursive types, custom runtime adapters, identity policies, mutation observations, and generic Class Mode are implemented and tested.

The design update is documented in [`docs/function-mode-design.md`](docs/function-mode-design.md).

YexJudge must be treated as a language runtime and code generator, not as a collection of LeetCode solutions. Algorithmic categories such as dynamic programming, greedy, graph, tree, or hashing must never influence harness generation.

### Phase 1A: Design checkpoint — approved

The design in [`docs/function-mode-design.md`](docs/function-mode-design.md) is the basis for implementation. Its execution-mode, recursive-type, observation, and runtime-type boundaries remain the architectural constraints for the work below.

Approved review points:

- explicit execution modes: stdin, function, class, interactive, SQL, and shell
- independent mode and language-backend registries
- recursive `TypeRef` parsing instead of manually registering every container combination
- generic observations for return values, mutated parameters, object graphs, and identity
- canonical output and comparison contracts
- registration of hidden runtime types without changing the core generator
- backward compatibility for the current function payload

### Phase 1B: Contract and recursive type foundation — complete

- parse and normalize recursive C++ types such as `vector<vector<int>>` and qualified references
- replace complete-string type switches with recursive type resolution
- preserve current primitive/vector behavior through a compatibility layer
- support generic `vector<T>` factories rather than registering each vector combination manually
- add mode-independent execution contracts and a mode registry
- add explicit mode validation while preserving legacy mode inference
- keep future modes registered at the contract level but reject them at the current API until implementations exist

### Phase 1C: Function Mode lifecycle — complete for scalar/vector observations

Function Mode is now split into generic stages:

1. emit helper/support declarations
2. construct typed inputs
3. invoke the requested member function
4. observe the return value and declared parameter mutations
5. emit canonical observations
6. canonicalize expected observations through the existing verdict path

The implementation supports scalar/vector functions, in-place mutation, prefix views driven by an integer return value, and `void` functions with parameter observations. No problem-specific branches were added.

Generic structural and identity postcondition policies remain in Phase 1D with custom runtime types.

### Phase 1D: Custom runtime types and additional modes — complete for initial C++ scope

Initial Function Mode scope is intentionally limited to C++. The shared execution contracts, observations, validation, comparison pipeline, queue, sandbox, and verdict logic are language-independent, but each language needs its own source-generation/runtime backend.

Completed in the current C++ slice:

- registered runtime adapters for `ListNode*`, `TreeNode*`, `RandomListNode*`, LeetCode-style `Node*`, and explicit `GraphNode*`
- canonical construction and serialization for linked lists, binary trees, random-pointer lists, and index-based graphs
- null/empty normalization, recursive `optional<T>`, and recursive `vector<T>` composition with runtime types
- generic `disjoint` and `same_as` identity postconditions emitted from metadata rather than problem-specific code
- graph topology validation that rejects unreachable nodes from the declared root
- generated-C++ tests for reverse-list, tree transformation, and random-list cloning
- generic C++ Class Mode with constructor metadata, operation sequences, typed arguments, and canonical results
- exported runtime-adapter registration contract for adding hidden C++ types independently
- class-mode tests covering stateful mutation, `void` results, repeated operation argument bindings, and invalid operation calls

Post-Phase 1 C++ backlog:

These are incremental C++ extensions, not a separate phase that blocks the production milestone. Implement them when a concrete YexCode metadata contract or representative test requires them; otherwise keep the current generic interfaces stable.

- broaden nullable/value-wrapper coverage beyond `optional<T>`; schedule before Phase 9 only if the supported C++ problem set needs it
- add additional topology policies beyond `disjoint` and `same_as` when a concrete platform contract requires them; the current identity policies already cover deep-copy and alias checks such as Copy List with Random Pointer
- add further hidden runtime types such as specialized readers, nested values, and alternate graph schemas incrementally, before enabling problems that use them
- metadata-driven Class Mode is already available now for constructor/operation contracts; add representative LRU Cache, Min Stack, and Trie metadata fixtures and end-to-end tests before Phase 9, without creating problem-specific generators

For the current roadmap:

- Defer Function/Class Mode backends for Python and Java until the complete C++ roadmap is stable.
- Do not plan a Go Function/Class backend at this time; revisit Go as a product decision after the C++ roadmap, with removal preferred over adding another backend unless a concrete requirement justifies keeping it.
- Reuse the mode/type/observation contracts when Python or Java backends are eventually added; do not repeat the judge service, queue, sandbox, or verdict work.
- Add Interactive Mode only when a protocol contract is defined.
- Keep SQL and Shell in separate runtimes.

Definition of done for Phase 1:

- the package boundaries and interfaces are documented
- recursive type resolution is specified
- mutation, structural observations, and identity policies are specified
- adding a normal problem requires only YexCode metadata and testcases
- new YexJudge code is required only for a new execution mode or runtime type
- no problem-specific generator is permitted
- C++ Function Mode supports scalar/vector, linked-list, tree, random-pointer, graph, nullable, mutation, and identity cases
- C++ Class Mode supports generic constructor/operation sequences without named-problem logic
- generated-C++ tests cover returned structures, custom-type mutation, deep-copy disjointness, alias rejection, graph topology, and class state transitions

## Phase 2: Build a Real Test Baseline

Status: complete; unit, Postgres, and opt-in API/Docker integration coverage are implemented.

Goal: make the current working behavior safe to change.

### Runtime verification prerequisite

Before adding more infrastructure, verify the current universal runtime path end to end:

- build `yexjudge-runtime:latest`
- start the server locally
- submit working and failing programs for C, C++, Python, Go, and Java
- verify `POST /submissions` returns `202 Accepted`
- verify `GET /submissions/{id}` transitions through `queued`, `running`, and `finished`
- verify accepted, wrong answer, runtime error, compilation error, and timeout verdicts

Also verify reusable sandbox behavior:

- tar-based staging copies source and compiled artifacts correctly
- sandbox restart clears `/workspace` and `/tmp`
- repeated submissions do not leak files or processes
- memory limits are applied before execution
- the runtime image contains every command required by the language registry

Fix any issues found here before relying on the runtime pool for later changes.

Work:

1. Add unit tests for `ValidateJob`.
   - limits, source-size limits, testcase-size limits, function metadata, and unsupported types

2. Add unit tests for testcase verdict mapping.
   - accepted, wrong answer including `actualOutput`, runtime error, timeout, and compilation error

3. Add unit tests for C++ function harness generation.
   - existing scalar/vector support first, then every adapter from Phase 1

4. Add Postgres integration tests.
   - save, get, update, concurrent claims, and no duplicate claim under multiple workers

5. Add API integration tests against a temporary database and Docker runtime image.
   - `POST /submissions` then polling result
   - `POST /submit` completed response and timeout response
   - `GET /diagnostics` aggregate operational response

Definition of done:

- `go test ./...` covers the important verdict and validation paths
- a documented integration command verifies Docker and Postgres behavior locally
- regressions in function drivers, queue claims, and result serialization are caught automatically

## Phase 3: Harden Execution

Status: **complete** for the current API and Docker execution scope.

Goal: safely expose the judge beyond local use without making the architecture complex.

Work:

1. Harden compile containers in `executor.go`.
   - disable networking
   - add CPU, memory, PID, filesystem, and timeout limits
   - run as an unprivileged user where compatible with the compiler images
   - cap compiler stdout and stderr retained in responses
   - distinguish user compilation errors from compiler/container infrastructure failures
   - treat nonzero Docker lifecycle and staging exits as infrastructure failures with capped diagnostics

2. Add output limits to runtime execution.
   - stop retaining unbounded stdout or stderr in memory
   - return a clear output-limit verdict or infrastructure error
   - cancel the execution and restart the reusable sandbox after an output-limit breach

3. Add basic request protections.
   - strict JSON decoding that rejects trailing values and unknown fields
   - reject request bodies over 1 MiB
   - return request IDs and structured JSON errors
   - authentication and per-user rate limits only when the application has users

4. Harden reusable sandbox lifecycle.
   - verify bounded readiness after startup and every restart before reuse
   - do not reset a sandbox twice when execution already restarted it
   - replace sandboxes when reset or readiness checks fail
   - persist terminal infrastructure failures as structured `infrastructure_error` results

Definition of done:

- compilation and execution both run with explicit resource and network restrictions
- oversized output cannot exhaust server memory
- all execution requests use the durable worker path and cannot bypass queue fairness
- API/Docker acceptance passes repeatedly with the documented Postgres URL and leaves no disposable test sandboxes
- Phase 4 remains responsible for recovering submissions left in `running` after a process crash

Verification completed on 2026-08-21:

- `GOCACHE=/tmp/yexjudge-go-cache go test ./...`
- `GOCACHE=/tmp/yexjudge-go-cache go test -race ./...`
- `GOCACHE=/tmp/yexjudge-go-cache go vet ./...`
- Postgres-backed `go test ./internal/judge -count=1`
- Three consecutive `TestAPIIntegration` runs against the provided Postgres URL, Docker images, one worker, and one sandbox
- No running disposable `yexjudge-*` test sandboxes remained; the existing Postgres container was not changed

## Phase 4: Make the Durable Queue Recoverable

Status: **complete** for the current Postgres-backed worker architecture.

Goal: ensure a server or worker crash cannot strand submissions forever.

Work:

1. Extend the `submissions` schema.
   - add `started_at`, `attempt_count`, `lease_expires_at`, and failure metadata
   - keep `db/submissions.sql` or its migrations synchronized with the store and queue code
   - introduce migration files instead of manually editing one schema file
   - use an attempt counter and failure message as the ownership/failure fence

2. Claim jobs with a lease.
   - a worker atomically claims `queued` work and sets a lease deadline
   - a worker extends the lease while processing long jobs if needed
   - fence renewals and final updates by the claimed attempt number

3. Recover expired work at startup and periodically.
   - requeue jobs with expired leases up to a retry limit
   - mark jobs failed after the retry limit, with a clear infrastructure error message
   - preserve retry metadata and make terminal recovery failures visible in the stored result

4. Make status transitions explicit.
   - `queued -> running -> finished`
   - `queued/running -> failed` only for infrastructure failures
   - retain final judge verdicts under `finished`

Definition of done:

- killing a worker during a job does not leave that job permanently `running`
- concurrent workers still cannot process the same attempt twice
- retry behavior is visible in the stored submission metadata and logs

Verification completed on 2026-08-22:

- Postgres lease, retry, recovery, claim-uniqueness, and stale-attempt fencing tests passed.
- Startup recovery was verified end to end by inserting an expired `running` submission before the API server started; it was requeued and completed.
- Three consecutive `TestAPIIntegration` runs passed with one worker and one runtime sandbox.
- `GOCACHE=/tmp/yexjudge-go-cache go test ./...` passed.
- `GOCACHE=/tmp/yexjudge-go-cache go test -race ./...` passed.
- `GOCACHE=/tmp/yexjudge-go-cache go vet ./...` passed.
- No running disposable `yexjudge-*` containers remained, and the existing Postgres container was not changed.

## Phase 5: Add Operational Visibility

Status: **complete** for the current single-process deployment.

Goal: make performance and failures explainable before adding automatic scaling.

Work:

1. Add structured logs.
   - submission ID, language, worker ID, status transitions, compile time, runtime, and infrastructure failures
   - emit JSON logs through the standard library `slog` handler

2. Measure the important timings.
   - queue wait duration
   - compile duration
   - sandbox acquisition wait duration
   - staging duration
   - testcase execution duration
   - sandbox reset duration
   - retain bounded cumulative histograms in process memory

3. Expose lightweight metrics or a protected diagnostics endpoint.
   - queued/running/failed submission counts
   - busy and available sandbox counts
   - worker busy count
   - runtime and compile latency histograms
   - expose the aggregate snapshot through `GET /diagnostics`

4. Update `architecture.md` and `README.md` whenever a behavior or public route changes.
   - document `POST /submit` as the combined convenience endpoint; it returns `200` when complete and `202` with an ID when polling is required.

Definition of done:

- a slow submission can be traced from API receipt to final result
- sandbox loss, queue backlog, and compiler slowdown are visible without adding temporary debug logging

Verification completed on 2026-08-23:

- Structured JSON logs now cover queueing, claims, status transitions, submission identity, language, worker, attempt, infrastructure failures, and phase durations.
- `/diagnostics` reports aggregate queued/running/failed counts, worker busy state, sandbox availability, and bounded latency histograms.
- Queue wait, compile, sandbox acquisition, staging, testcase/runtime, and sandbox reset timings are collected and tested.
- `GOCACHE=/tmp/yexjudge-go-cache go test ./...` passed.
- `GOCACHE=/tmp/yexjudge-go-cache go test -race ./...` passed.
- `GOCACHE=/tmp/yexjudge-go-cache go vet ./...` passed.
- Postgres-backed tests passed, including lease/recovery coverage.
- Three consecutive `TestAPIIntegration` runs passed with the diagnostics endpoint enabled.
- No running disposable `yexjudge-*` containers remained, and the existing Postgres container was not changed.

## Phase 6: Improve Fixed Capacity First

Goal: establish safe manual capacity controls before adaptive behavior.

Work:

1. Separate the capacities conceptually.
   - worker count limits concurrent submission orchestration
   - sandbox pool size limits concurrent runtime execution
   - compile concurrency needs its own limit because compile containers are not pooled yet

2. Add configuration and validation.
   - minimum and maximum values for workers, sandboxes, and compile slots
   - reject unsafe configurations that exceed a chosen memory budget
   - document recommended local defaults

3. Add a compile semaphore.
   - prevent a burst of compiled submissions from launching unbounded Docker compile containers
   - preserve fair progress for interpreted languages where possible

4. Harden the runtime pool lifecycle.
   - clean up warm sandbox containers during graceful shutdown
   - replace unhealthy sandboxes without silently reducing capacity
   - keep pool capacity and worker configuration observable

Definition of done:

- all expensive execution paths have explicit concurrency bounds
- the server remains responsive during a burst larger than its sandbox pool
- sandbox loss and graceful shutdown do not permanently reduce usable capacity

## Phase 7: Add Conservative Adaptive Capacity — deferred

Status: intentionally deferred until after the resume-ready Phase 9 milestone. Fixed worker, sandbox, and compile capacity from Phase 6 is sufficient for the first production version.

Goal: adjust capacity according to load without destabilizing the host.

Precondition: complete Phases 4 through 6 first. Autoscaling without recovery, limits, and metrics only hides overload.

Initial policy:

1. Keep a fixed minimum number of workers and warm sandboxes.
2. Increase capacity only when queue depth and queue wait time stay above thresholds.
3. Decrease capacity only after a sustained idle period.
4. Never exceed a configured maximum determined from host CPU and memory.
5. Treat compile slots separately from runtime sandboxes.

Signals to use:

- queued count and oldest queue age
- worker busy count
- sandbox availability and acquisition wait time
- host memory headroom
- recent compile/runtime duration by language

Definition of done:

- burst traffic increases usable capacity within configured bounds
- idle capacity returns toward the minimum without interrupting active jobs
- memory pressure prevents scale-up and is recorded clearly

## Phase 8: Reduce Compile Latency Carefully

Goal: remove the largest remaining per-submission overhead without weakening isolation.

Work:

1. Use the timings from Phase 5 to confirm compilation is the meaningful bottleneck.
2. Keep compile and runtime environments separate.
3. Choose one implementation:
   - bounded reusable compile containers keyed by toolchain image, or
   - dedicated compile workers with clean workspaces per job
4. Ensure the compile environment is reset, output-bounded, network-disabled, and resource-limited after every job.
5. Benchmark cold and warm compilation before and after the change.

Definition of done:

- compiled-language latency improves measurably under repeat load
- no compiler artifacts or processes survive between jobs
- compilation isolation remains at least as strong as the current one-off container path

## Phase 9: Production Delivery

Goal: package the project as a reliable deployable service.

Work:

1. Add migrations and environment validation at startup.
2. Add Docker Compose for Postgres plus YexJudge local deployment.
3. Add graceful shutdown behavior.
   - stop accepting new work
   - allow bounded in-flight completion
   - return or remove sandbox containers
   - release database connections cleanly
4. Add health and readiness checks.
   - health: process is alive
   - readiness: Postgres reachable, runtime image present, minimum sandbox capacity available
5. Add a small load-test script and record baseline performance in the README.

Definition of done:

- a new developer can start the full service with documented commands
- deployment can detect when the process is alive but not safe to accept submissions
- performance claims are backed by a repeatable local benchmark

## Phase 10: Add Deferred Language Backends and Resolve Go Support — deferred

Status: intentionally deferred until after the C++-first resume-ready milestone. Python and Java stdin/stdout support remain available, but their Function/Class backends will not be added during the current delivery target. Go receives no new driver work while its keep/remove decision remains open.

Precondition: complete the C++ custom runtime types, Class/Interactive decisions, execution hardening, queue recovery, observability, capacity, and production-delivery work above.

Goal: add Python and Java support deliberately, only after the shared C++-validated contracts are stable. Do not repeat queue, worker, sandbox, validation, observation, or verdict infrastructure.

Work:

1. Define the Python Function Mode backend.
   - preserve the same mode-independent contract and observation model
   - generate Python drivers from recursive metadata rather than problem names
   - define Python-specific conventions for `Solution`, type construction, serialization, mutation, and identity

2. Define the Java Function Mode backend.
   - decide the accepted `Solution` and method-signature conventions
   - generate Java drivers from the shared contract
   - define Java-specific serialization and runtime-type registration rules

3. Add backend-focused tests and end-to-end coverage.
   - test each backend independently before enabling it in the public API
   - verify that custom runtime types and comparison policies remain generic

4. Make an explicit Go product decision.
   - remove the Go language option and its runtime/image documentation if no concrete requirement remains, or
   - retain it as stdin/stdout-only with a documented maintenance boundary
   - do not begin Go Function/Class Mode work unless a new requirement reverses this decision

Definition of done:

- Python and Java Function Mode are implemented through separate backends without changes to the shared judge pipeline
- adding a normal problem still requires only metadata and test cases
- Go has an explicit keep/remove outcome and no ambiguous roadmap status

## Decision Log

Keep these principles unless a measured requirement changes them:

- Postgres remains the durable submission store and queue for now.
- Runtime sandboxes remain reusable and separate from compile environments.
- One sandbox is used for all test cases of one submission.
- `POST /submissions` plus `GET /submissions/{id}` is the primary production API.
- `POST /submit` is a bounded convenience API, not the high-throughput default.
- `POST /judge` remains a temporary compatibility alias for submission creation while clients migrate to `POST /submissions`.
- `POST /submissions` plus `GET /submissions/{id}` remain the primary submission-oriented API.
- Function-mode harnesses must be data-driven and adapter-based rather than a custom driver per problem.
- C++ is the only active Function/Class Mode implementation target until the C++ roadmap is complete.
- Python and Java are deferred final language-backend work; they must reuse the shared contracts rather than duplicate judge infrastructure.
- Go receives no new feature investment while its eventual removal is evaluated; the existing stdin/stdout path remains available until that decision is made.
