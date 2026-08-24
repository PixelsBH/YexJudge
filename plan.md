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
- C, C++, Python, Go, and Java stdin/stdout execution
- C++ metadata-driven Function Mode and Class Mode
- recursive C++ types, custom runtime adapters, mutation observations, and identity postconditions
- structured logs, `/diagnostics`, `/health`, and `/ready`
- automatic schema/migration application, `.env` configuration, Docker Compose, and a repeatable load-test script

See [`architecture.md`](architecture.md) for the full system description, package boundaries, security model, API behavior, and validation evidence.

## Future Work 1: Adaptive Capacity

Formerly Phase 7. Intentionally deferred until measured demand requires it.

### Goal

Adjust worker, compile-worker, and runtime-sandbox capacity according to load without destabilizing the host.

### Constraints

- fixed minimum capacity must remain available
- scale-up must respect configured CPU and memory limits
- scale-down must not interrupt active submissions
- compile workers and runtime sandboxes must be scaled independently
- queue recovery, execution limits, and diagnostics must remain intact

### Possible signals

- queue depth and oldest queue age
- queue wait duration
- submission-worker busy count
- compile-worker utilization and wait time
- sandbox availability and acquisition wait time
- host memory headroom
- recent compile and runtime duration by language

### Definition of done

- burst traffic increases usable capacity within configured bounds
- idle capacity returns toward the minimum without interrupting active jobs
- memory pressure prevents unsafe scale-up and is visible in diagnostics/logs
- capacity changes are covered by deterministic tests

## Future Work 2: Additional Language Backends and Go Decision

Formerly Phase 10. Intentionally deferred until a concrete product requirement exists.

### Goal

Expand metadata-driven Function/Class Mode without duplicating the shared judge infrastructure.

### Python Function/Class Backend

- reuse the mode-independent execution, observation, validation, and comparison contracts
- define Python-specific `Solution` and method-signature conventions
- generate drivers from recursive metadata rather than problem names
- define Python serialization, mutation, identity, and custom-runtime rules
- add backend-focused unit and end-to-end tests before public enablement

### Java Function/Class Backend

- decide accepted `Solution` and method-signature conventions
- generate Java drivers from the shared contracts
- define Java serialization, mutation, identity, and custom-runtime rules
- add backend-focused unit and end-to-end tests before public enablement

### Go Product Decision

Choose one explicit outcome:

- remove Go and its stdin/stdout documentation if no concrete requirement remains, or
- retain Go as stdin/stdout-only with a documented maintenance boundary

Do not add Go Function/Class Mode unless a new requirement reverses the current decision.

### Definition of done

- Python and Java Function/Class backends use separate language backends without changes to the shared queue, worker, sandbox, validation, observation, or verdict infrastructure
- adding a normal problem still requires only metadata and test cases
- Go has an explicit keep/remove outcome

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
- adaptive capacity must not be introduced before there is measured demand
