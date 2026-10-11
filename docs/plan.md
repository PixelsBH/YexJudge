# YexJudge Future Roadmap

## Purpose

YexJudge has reached its primary resume-ready milestone: a C++-first asynchronous online judge with metadata-driven Function/Class Mode support, durable queue recovery, isolated Docker execution, operational visibility, bounded capacity, compile workers, and local production packaging.

The completed implementation is documented in [`architecture.md`](architecture.md). This file intentionally contains only deferred work and optional extensions; it is not a history of completed phases.

## Completed Baseline

The current service provides:

- asynchronous `POST /submissions` with `GET /submissions/{id}` result retrieval
- synchronous `POST /submit` with bounded waiting
- PostgreSQL-backed durable storage and queue claiming
- attempt-fenced leases, recovery, and bounded retries
- fixed submission workers, dedicated compile workers, and reusable runtime sandboxes
- restricted disposable compilation and bounded execution output
- C, C++, and Python stdin/stdout execution
- C++ and Python metadata-driven Function Mode and Class Mode
- recursive C++ and Python values, custom runtime adapters, mutation observations, and identity postconditions
- structured logs, `/diagnostics`, `/health`, and `/ready`
- automatic schema/migration application, `.env` configuration, Docker Compose, and a repeatable load-test script

See [`architecture.md`](architecture.md) for the full system description, package boundaries, security model, API behavior, and validation evidence.

## Future Work 1: Single-VPS Deployment and Vulnerability Maintenance

YexCode and YexJudge will run on the same VPS behind a reverse proxy for a personal/demo deployment. Keep the deployment simple and preserve the existing execution isolation; multi-host scaling is not planned.

### Deployment topology

```text
Internet
   |
Reverse proxy (TLS)
   |
YexCode
   | private local connection
YexJudge
   |                  |
PostgreSQL       Container runner
```

### Deployment and security maintenance

- Use the reverse proxy as the public entry point, terminate TLS there, and configure reasonable connection and request timeouts.
- Keep the YexJudge service port, PostgreSQL, and container-engine control socket private to the VPS; do not expose them directly to the public network.
- Run YexCode and YexJudge as restartable services and verify health/readiness checks after deployment.
- Preserve compile/runtime isolation and resource limits: no privileged containers, host networking/devices, or submission-selected host mounts; no network access from submitted code; bounded CPU, memory, PIDs, time, output, and workspace; separate compile and runtime environments.
- Review Go module dependencies, the host OS, the container engine, and compiler/runtime images for known vulnerabilities during updates. Apply relevant patches, rebuild affected images, and rerun the validation suite.

### Definition of done

- Both applications run on one VPS, with only the reverse proxy exposed publicly and YexJudge, PostgreSQL, and container-engine control kept private.
- The deployed sandbox configuration retains the tested isolation and execution limits.
- Dependency and image vulnerability checks are part of maintenance, and actionable findings are patched or have a documented update plan.

## Future Work 2: Migrate from Docker to Podman

Replace Docker completely with Podman for building and running YexJudge compile and runtime containers, local deployment, and project tooling.

### Constraints

- Preserve compile/runtime separation, disposable environments, sandbox reset and replacement, cancellation, and current resource/output limits.
- Preserve or strengthen network, filesystem, capability, privilege, and process isolation; verify actual Podman behavior instead of assuming Docker API compatibility guarantees equivalent restrictions.
- Remove Docker-specific runtime, build, Compose, test, and documentation dependencies once their Podman replacements are validated.

### Definition of done

- YexJudge builds and runs its compiler and runtime images and manages containers using Podman without requiring a Docker daemon, CLI, socket, or Docker Compose.
- Integration tests cover compilation, execution, cleanup/reset, cancellation, and enforcement of sandbox limits under Podman.
- Deployment files, scripts, setup instructions, and operational docs consistently use Podman; no Docker-only dependency remains in the supported workflow.

## Future Work 3: PostgreSQL Queries with pgx and sqlc

The current application uses pgx through `database/sql` and handwritten SQL. Move application query code to generated, typed sqlc methods backed by native pgx APIs while retaining PostgreSQL and the existing migration approach.

### Constraints

- Keep SQL query sources reviewable and sqlc generation reproducible.
- Preserve claim uniqueness, lease fencing, recovery, transaction boundaries, and existing persistence behavior.
- Keep schema migrations as ordered SQL migration files; sqlc generates typed query code but does not replace migration execution.

### Definition of done

- Queue/store queries and row mappings use sqlc-generated APIs backed by pgx; runtime PostgreSQL access, including migration/bootstrap paths, uses native pgx APIs without the `database/sql` adapter.
- Generated code can be reproduced from checked-in configuration and SQL sources, and CI or validation detects stale generated output.
- PostgreSQL integration tests continue to cover concurrent claims, leases, retries, and persistence behavior.

## Future Work 4: Project Simplification and Memory Footprint

Review the project for dead or redundant code, unnecessarily complex implementations, and high-cost data structures. Prefer the simplest implementation that preserves current contracts, and use profiles and measurements to target memory reductions.

### Constraints

- Remove code only after confirming it is unused or behaviorally redundant; avoid broad rewrites without a clear maintenance or performance benefit.
- Base struct/layout and allocation changes on representative heap profiles or benchmarks, prioritizing high-volume paths.
- Preserve validation, concurrency, recovery, isolation, and API behavior.

### Definition of done

- Dead code and avoidable duplicate work are removed or consolidated with focused regression tests.
- Representative benchmarks or heap profiles document meaningful memory/allocation improvements for any performance-motivated changes.
- The full test suite and race checks pass, with no regression in correctness or judge behavior.

## Optional C++ Extensions

These are incremental additions, not blockers for the completed milestone:

- add representative metadata fixtures and end-to-end tests for Min Stack and Trie; LRU Cache coverage is now present in the C++ harness and API integration suites
- broaden nullable/value-wrapper support beyond `optional<T>` when required by YexCode metadata
- add topology policies beyond `disjoint` and `same_as` when a concrete contract requires them
- add specialized readers, nested values, and alternate graph schemas as independent runtime adapters
- define Interactive Mode when a complete protocol contract exists
- keep SQL and Shell in separate runtimes

## Architectural Constraints

These principles remain in force for all future work:

- algorithmic categories must never influence harness generation
- problem-specific generators are not permitted
- execution modes and recursive data types are the primary extension points
- custom runtime types must be registered independently of the core harness generator
- C++ compile and runtime environments remain separate
- untrusted submissions must not share compiler state or artifacts across jobs
- future language backends must reuse the shared judge pipeline
