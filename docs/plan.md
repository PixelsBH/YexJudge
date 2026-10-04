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

Intentionally deferred until measured demand requires it.

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

Intentionally deferred until a concrete product requirement exists.

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

**Status: paused after implementation; not launch-approved.** The current security implementation is retained. Continue with the unchecked work below before accepting untrusted public submissions. See [`production-security.md`](production-security.md) for deployment configuration, verification commands, and operational runbooks. Future Work 1 and 2 remain deferred.

### Implemented code and packaging (not host certification)

- Disposable compilers use read-only source mounts and bounded tmpfs; allowlisted, bounded regular artifacts are imported through a strict archive protocol instead of host-writable compiler output mounts.
- Compile/runtime isolation includes private namespaces, network `none`, read-only roots, dropped capabilities, unprivileged users, resource/output/filesystem limits, default seccomp, disabled container output logging, and restart/replacement cleanup. Docker CLI cancellation reaps local process groups; host parsing/harness work is bounded.
- PostgreSQL has transactional queue/service/user admission, owner-scoped lookup/terminal deletion, batched terminal retention, and bounded operations. These are wired into HTTP admission and an hourly/startup retention scheduler. A separate `cmd/migrate` supports its own secret file, verified production TLS, serialized migration application, and schema checks.
- `docker-compose.production.yml` is a **standalone** manifest, never a development override: no bundled database/default passwords, no project checkout mount into judge, numeric user plus socket supplementary group, dedicated same-path workspace bind, protected external secret files, pinned server/proxy/execution image inputs, explicit migration profile, restricted private HTTP, TLS nginx, loopback-only admin ingress, resource/time/body/connection limits, supervision/readiness/graceful shutdown configuration.
- `scripts/verify-production.py` fails closed on missing deployment configuration and unsafe topology/settings. Host checks and opt-in Docker containment regressions / mutating TLS endpoint smoke are separate from syntax checks; skipped tests and absent prerequisites are not launch evidence. Image scans, real database integration, privilege tests, firewall, quotas, restore and incident drills remain external gates.

- Server integration now includes rotatable bearer credentials and separate submit/admin roles; gateway-derived `X-Yex-User-ID`; cryptographically random submission IDs; service/user-scoped result retrieval and deletion; minimally public `/health`; admin-only `/ready` and `/diagnostics`; exact Host validation; browser Origin rejection; bounded request/admission rates and in-flight requests; production file-backed secrets, verified PostgreSQL TLS, schema checking without automatic migrations, digest-pinned execution images, result-detail redaction, retention scheduling, and Go HTTP timeouts.
- Regression tests cover authentication, roles, ownership, rotation, redaction, quota responses, rate-limiter cardinality, in-flight capacity, host/Origin handling, local loopback restrictions, and production configuration. Passing unit tests does not prove production containment or host security.

### Resume here: remaining Priority 0 integration work

- [ ] Review the entire integrated change set, especially the new compiler artifact/export path, per-testcase restart/restore lifecycle, authentication middleware, admission transactions, and production image compatibility. No independent final security review has been completed.
- [ ] Update development Compose for the image's new unprivileged default, explicit Docker socket supplementary group, workspace ownership, and authenticated API configuration. The existing development Compose does not yet satisfy these requirements; do not use it as a production base.
- [ ] Update `.env.example`, README, `operations.md`, `usage.md`, `architecture.md`, and the load-test client to match the new authentication/user identity/result-redaction contract. Existing local examples still describe the old unauthenticated behavior. Production instructions live in `production-security.md`.
- [ ] Update `cmd/server/integration_test.go`: explicitly configure authenticated requests (or development-only loopback mode), seed the stale recovery fixture with the intended owner, and run against the new migrated schema. This existing Docker/API suite is gated and has not been run against the integrated server.
- [ ] Integrate YexCode: keep service credentials server-side, derive the stable opaque user ID from its authenticated account, overwrite/reject browser-supplied identity headers, omit browser Origin, and handle `401`, `403`, `404`, `409`, `429`, `503`, and `Retry-After`. New secure startup intentionally refuses missing service credentials; insecure local mode requires explicit opt-in and a loopback listener.
- [ ] Exercise slow headers/bodies/responses, oversized headers/chunked JSON, concurrent synchronous submits, database stalls, repeated infrastructure failure, graceful shutdown, and restart/lease recovery using the actual server and reverse proxy. Review numeric duration settings before conversion to `time.Duration` for overflow-safe bounds and verify synchronous waiting stays bounded during slow DB operations.
- [ ] Measure fairness under adversarial load. Per-user/service active-job caps and compile-request rates are implemented, but strict fair scheduling/reservation of compile slots and workers is not. One authenticated service is trusted to convey user identity correctly.
- [ ] Keep a single API instance initially: HTTP rate budgets are process-local and reset on restart; durable active-job/queue admission is database-coordinated. Add gateway/shared rate coordination before scaling API replicas.

### Resume here: remaining Priority 0 staging and launch verification

- [ ] Provision a dedicated patched Linux host with no unrelated workloads; validate Docker socket group/UID ownership, secure workspace ancestors, bounded filesystem/quota, SSH/admin separation, and host/cloud firewall rules (including IPv6 and Docker's own NAT/forwarding behavior).
- [ ] Confirm only gateway HTTPS ingress is exposed. Verify judge HTTP, admin TLS, PostgreSQL, and Docker TCP ports are unreachable from unauthorized machines. Docker socket authority remains host-level authority; the current monolith is not a complete host-compromise boundary.
- [ ] Supply protected credentials, PostgreSQL CA, TLS chain/key, exact allowed hosts, and reviewed digest-pinned images. Build and scan the actual server/runtime/compiler/proxy images; validate no execution-image Dockerfile volumes and compiler/runtime ABI compatibility.
- [ ] Build the release images and run `nginx -t` against the actual proxy image/certificates. Only standalone production Compose syntax/rendering has been checked; image builds and live TLS/proxy checks have not.
- [ ] Run `scripts/verify-production.py` against completed deployment configuration, then its opt-in Docker tests and mutating TLS endpoint smoke on isolated staging. A config-only pass, skipped test, or placeholder digest is not launch evidence.
- [ ] Run `YEXJUDGE_DOCKER_SECURITY=1 go test ./internal/judge -run '^TestDockerSecurity' -v -count=1` with reviewed locally installed image overrides. Docker socket access is denied in the current environment, so filesystem/network/privilege/fork/FD/memory/disk/output/timeout/artifact/reset and language compatibility regressions remain unexecuted here.
- [ ] Independently assess container-escape risk; containment tests cannot prove that Docker eliminates it. Rootless Docker, an API proxy, and eventual API/worker separation need separate threat-model and compatibility reviews, not blanket security claims.
- [ ] Prove production-like abuse/queue-full behavior, cross-user and cross-service isolation, admin route isolation, gateway identity handling, hidden-test-safe responses, and log redaction. Test token overlap, revocation, and stable-owner access through rotation with the actual gateway.

### Resume here: remaining Priority 1 data protection

- [ ] Provision distinct least-privilege application and migration PostgreSQL roles; prove application DML/schema-check access and negative DDL/admin permissions in staging. Application startup must only check schema in production.
- [ ] Review and run numbered migrations with separate credentials, a backup/PITR checkpoint, and a documented compatibility/rollback plan. Existing unowned rows stay inaccessible to secured APIs; decide whether to purge them or perform a controlled ownership backfill (never infer owners from request input).
- [ ] Verify private database routing, certificate/hostname validation and no TLS fallback. Do not treat URL parsing or a manifest network name as proof of network isolation.
- [ ] Deploy the protected secret store/secret manager; verify file ownership/modes and absence of secrets in Git, build contexts, arguments, logs, CI artifacts, and browser bundles. Recreate containers after replacing file-backed secrets and demonstrate revocation.
- [ ] Verify startup/hourly terminal-only retention and scoped deletion with the application role, active-job conflicts, bounded batching, monitoring, and recovery after downtime. Establish separate expiry/deletion policies for WAL, replicas, backups, exported results, and legal holds.
- [ ] Encrypt backups, restrict key/access ownership, test isolated restores, record RPO/RTO, and reapply deletion records after restoration. Backups and restore drills are not provisioned by this repository.

### Resume here: remaining Priority 1 operational security

- [ ] Establish a reviewed patch/rebuild/scan/sign/SBOM process for the OS, Docker, Go, compiler/runtime/proxy/base images, and dependencies; schedule updates and rehearse rollback. Release image digests must be real reviewed artifacts, not syntax-valid placeholders.
- [ ] Configure monitoring/alerts for queue age/depth, auth/rate/capacity failures, retention failures, worker/compile/sandbox utilization, lease recovery, repeated replacements/infrastructure failures, output limits, DB saturation, host memory/disk pressure, and certificate expiry. Aggregate authenticated diagnostics exist; alert delivery is not configured.
- [ ] Rehearse supervised startup/readiness, graceful termination, dependency outages, and orphan sibling-container/workspace reconciliation. Compose does not automatically restart unhealthy containers or clean up orphan execution containers.
- [ ] Assign incident ownership and test the runbooks: pause new admissions with `ACCEPT_SUBMISSIONS=false`, block gateway ingress, revoke compromised service/DB credentials, stop workers for full emergency shutdown, isolate the host, and restore safely. The admission switch does not stop already running jobs.
- [ ] Record release-specific staging evidence and obtain a final review before enabling public traffic. Do not label Future Work 3 complete until the host/database/gateway gates below have actual evidence.

### Validation checkpoint when paused

- Passed: `go test ./...`, `go test -race ./...`, `go vet ./...`, `git diff --check`, the 26 offline `scripts/test_verify_production.py` tests, and nginx/readiness shell syntax checks. Editor diagnostics report no errors or warnings.
- The persistence work was separately tested against temporary PostgreSQL with race-enabled admission/ownership/deletion/retention/lease tests. Repeat these using the exact staging database roles and release configuration; this is not proof of production grants or network/TLS setup.
- Not run against the integrated deployment: existing Docker/API integration suite, real Docker containment/language suites, image builds/scans, full live `nginx -t`/TLS smoke, gateway rotation/revocation, production database privilege/TLS tests, backup/restore, firewall/quotas, and incident drills. Gated integrations skipped in the default Go run; Docker socket permission is unavailable here.

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

The YexJudge host must not share a Docker daemon or host filesystem with unrelated applications. This production manifest requires external/managed PostgreSQL on a private network with verified TLS, separate application/migration roles, encrypted backups, and a tested restore. It deliberately does not bundle a production database.

### Priority 0: protect the public boundary

- verify the implemented rotatable service credentials with YexCode in staging
- prove unauthenticated submission creation and result access are rejected through the public proxy
- prove random result IDs remain scoped to the authenticated service/user across token rotation
- verify minimally public `/health`, admin-only `/ready` and `/diagnostics`, and loopback-only administrative proxy exposure
- validate the packaged reverse-proxy TLS termination, strict host handling, request/header timeouts, security headers, and loopback admin isolation using the actual reviewed image, certificate, firewall, and host
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
- run the opt-in filesystem/privilege/network, fork/FD/memory/disk exhaustion, output-flood, timeout, artifact and reset regressions on the actual dedicated host; separately assess container-escape risk, which these tests cannot disprove

### Priority 0: authentication, abuse, and denial of service

The authentication/identity/abuse contract is implemented in the server and covered by unit tests; integrated gateway and real-host abuse behavior must still be verified. Before public use:

- authenticate the YexCode service and establish how user identity is conveyed without trusting a client-supplied user ID
- validate implemented per-user/service rates, active-job limits, queue bounds, and in-flight request limits under real adversarial load
- verify queue-full `503` and rate/active-limit `429` responses and client backoff rather than database/Docker exhaustion
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
- use only the new standalone production manifest; never merge it with development Compose, and verify no development mounts/ports/passwords survive
- review and explicitly run one-shot migrations with separate credentials, a backup/PITR checkpoint and tested rollback/restore plan; application startup must check schema, not migrate production automatically
- restrict SSH and administrative access, use key-based access, and keep host administration separate from application credentials
- monitor queue depth, worker utilization, Docker failures, disk usage, memory pressure, authentication failures, and unusual submission volume
- alert on repeated sandbox replacement, lease recovery, infrastructure failures, output-limit events, and host resource exhaustion
- keep `/diagnostics` aggregate-only and authenticated; ensure logs and metrics do not become a source-code side channel

### Definition of done — still-open release/host gates

Code and manifest existence do **not** complete this checklist. Record evidence against the exact release, gateway, database and dedicated host; rootless Docker/API proxies and eventual API/worker separation remain assessed future work, not guarantees. The monolith's Docker-controlling worker can still compromise its host.

- only an authenticated YexCode service can create submissions or retrieve protected results
- the judge host is isolated, patched, firewalled, and has no publicly reachable Docker daemon or Docker socket
- PostgreSQL is private, TLS-protected, least-privileged, backed up, and restorable
- production execution matches the tested container restrictions and no user payload can control host-level Docker options
- per-client rate, concurrency, queue, memory, CPU, disk, and timeout limits are documented and enforced
- public health/readiness/diagnostics exposure is deliberate and does not reveal sensitive operational data
- secrets are stored outside Git and rotated successfully in a staging deployment
- container images and dependencies have a patch/update process and vulnerability review
- containment/abuse/denial-of-service/logging-redaction/authentication/ownership/rotation/database-concurrency/retention/recovery tests pass in a production-like environment, with container-escape risk explicitly assessed rather than claimed eliminated
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
