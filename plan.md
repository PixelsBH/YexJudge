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

## Future Work 3: Deployment Security and Production Hardening

Intentionally deferred until YexJudge is prepared for an internet-facing deployment. This work is required before accepting untrusted public submissions, even though the current compile and runtime containers already have important restrictions.

### Deployment target

The initial production topology should use an isolated Docker-capable VM or dedicated worker host rather than a serverless platform:

```text
YexCode / authenticated gateway
            |
       HTTPS service API
            |
     isolated YexJudge host
       |              |
   PostgreSQL      Docker runner
```

The YexJudge host must not share a Docker daemon or host filesystem with unrelated applications. PostgreSQL should remain on a private network, either on the same isolated host or as a managed database with TLS.

### Priority 0: protect the public boundary

- add service-to-service authentication for YexCode-to-YexJudge requests, preferably a rotatable secret or signed service credential
- reject unauthenticated submission creation and result access from the public internet
- decide whether result IDs are opaque, authenticated, and scoped to the requesting user/service
- keep `/health` minimally public if required by the hosting platform, but protect `/ready` and especially `/diagnostics` behind trusted-network or authenticated access
- add reverse-proxy TLS termination, strict host handling, request/header timeouts, and safe security headers
- configure CORS deliberately; the intended browser path is through YexCode, not direct browser access to YexJudge
- keep the existing strict JSON decoding, request-size limits, method checks, and request IDs

### Priority 0: reduce Docker-host risk

The current server controls Docker through `/var/run/docker.sock`. Access to that socket is effectively host-level authority, so it must not be exposed to clients or mounted into any public-facing component.

- run YexJudge on a dedicated, patched host with no unrelated workloads
- firewall the Docker daemon and Unix socket; never expose either on a TCP listener
- do not run the HTTP API with a broadly privileged host identity unless the execution design requires it
- assess rootless Docker or a narrowly scoped Docker API proxy as defense-in-depth; neither should be treated as a complete boundary without threat-model testing
- separate the public API/queue service from Docker-controlling execution workers when the deployment grows beyond one host
- ensure host-path workspace mounts cannot be selected or influenced by submission payloads
- verify that no user-controlled source, compiler output, or testcase metadata can select host mounts, container privileges, devices, capabilities, or Docker options
- document the residual risk that a compromise of the Docker-controlling worker may compromise its host

### Priority 0: preserve execution isolation

Before public enablement, verify the existing restrictions in production rather than relying only on local configuration:

- no privileged containers and no host network, PID namespace, IPC namespace, devices, or unrestricted host mounts
- no network access from compile and runtime containers unless a future language explicitly requires a separately reviewed exception
- read-only container roots, bounded temporary storage, dropped capabilities, `no-new-privileges`, unprivileged container users, and bounded PIDs
- explicit CPU, memory, execution-time, stdout/stderr, source, testcase, and workspace limits
- compiler and runtime environments remain separate
- fresh compile environments do not share artifacts or compiler state across submissions
- runtime sandbox reset/replacement continues to clear processes and temporary state
- runtime and compiler images are pinned to reviewed versions or digests, scanned for vulnerabilities, and rebuilt on a patch schedule
- add container-escape, filesystem, fork-bomb, output-flood, timeout, and resource-exhaustion tests to the deployment verification suite

### Priority 0: authentication, abuse, and denial of service

Authentication is currently deferred because the local service has no user/account model. Before public use:

- authenticate the YexCode service and establish how user identity is conveyed without trusting a client-supplied user ID
- add per-user and per-service rate limits, concurrent-job limits, queue limits, and bounded request rates
- define behavior when the queue is full; reject work predictably instead of exhausting Postgres or Docker capacity
- retain bounded retries and leases, with limits on repeated infrastructure failures
- apply connection, header, body, and response timeouts at the proxy and Go server
- protect expensive compilation separately from cheap request validation
- prevent one client from consuming all workers, compile slots, sandboxes, memory, disk, or Postgres connections
- define abuse handling, IP/service blocking, and operational emergency shutdown procedures

### Priority 1: secrets and data protection

- move production credentials, service tokens, database URLs, and signing keys into a secret manager or protected deployment secret store
- never log source code, database credentials, authorization headers, hidden test data, or full compiler output by default
- use separate least-privilege Postgres credentials for application access and migrations
- require TLS for managed Postgres connections and restrict database network access to YexJudge
- define source-code and result retention, deletion, and backup policies
- encrypt backups and test restoration before launch
- review whether failed-test details can reveal protected problem data and keep the public response contract intentional
- rotate credentials and document compromise/revocation procedures

### Priority 1: operational security

- run the service under a supervised, restartable process with graceful shutdown and readiness checks
- keep the operating system, Docker Engine, Go toolchain, compiler images, and base images patched
- use a production Compose override or equivalent deployment manifest with no development bind mounts, default passwords, or debug settings
- review migrations before applying them automatically in production and keep rollback/backup procedures
- restrict SSH and administrative access, use key-based access, and keep host administration separate from application credentials
- monitor queue depth, worker utilization, Docker failures, disk usage, memory pressure, authentication failures, and unusual submission volume
- alert on repeated sandbox replacement, lease recovery, infrastructure failures, output-limit events, and host resource exhaustion
- keep `/diagnostics` aggregate-only and authenticated; ensure logs and metrics do not become a source-code side channel

### Definition of done

- only an authenticated YexCode service can create submissions or retrieve protected results
- the judge host is isolated, patched, firewalled, and has no publicly reachable Docker daemon or Docker socket
- PostgreSQL is private, TLS-protected, least-privileged, backed up, and restorable
- production execution matches the tested container restrictions and no user payload can control host-level Docker options
- per-client rate, concurrency, queue, memory, CPU, disk, and timeout limits are documented and enforced
- public health/readiness/diagnostics exposure is deliberate and does not reveal sensitive operational data
- secrets are stored outside Git and rotated successfully in a staging deployment
- container images and dependencies have a patch/update process and vulnerability review
- escape, abuse, denial-of-service, logging-redaction, authentication, and recovery tests pass in a production-like environment
- an incident response and emergency shutdown procedure exists before public launch

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
