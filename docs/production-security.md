# Production deployment and security runbooks

**Status: packaging and verification tooling, not permission to launch.** This is the production security/runbook entry point for YexJudge. The development README/operations examples are not production instructions. A successful manifest render, unit test, or containment regression does not prove resistance to container escape or completion of the external launch gates below.

## Threat boundary and deployment prerequisites

Deploy only on a **dedicated, patched Linux VM/worker host**, with a local Docker Engine and no unrelated applications, containers, credentials, host directories, or Docker users. The current server is still a monolith: it authenticates HTTP requests, accesses PostgreSQL, and controls Docker. Docker socket access remains effectively host-level authority even when the process has UID `10001`, dropped capabilities, a read-only root, and `no-new-privileges`. A compromised judge can ask Docker to mount host files or start privileged containers; these flags do not restrict the daemon's authority. Do not mount the socket into nginx, a gateway, or a submission container. A read-only socket bind would not make Docker API access read-only.

```text
YexCode / authenticated gateway
    | HTTPS 443; bearer token + gateway-derived opaque user ID
    v
nginx (TLS, no socket) -- private HTTP --> judge (UID/GID 10001, Docker socket group)
                                              |                 |
                                   managed TLS PostgreSQL    isolated sibling
                                                             compile/runtime containers
Operations -- SSH tunnel --> loopback TLS 9443 --> admin-only ready/diagnostics
```

Required before deployment:

- A managed/external PostgreSQL database on private DNS/network, with verified TLS and an operator-supplied CA. There is **no bundled production database or default database password**.
- Distinct least-privilege application and migration database roles. The application needs the reviewed DML/schema-inspection grants, not schema ownership, DDL, role creation, superuser, or provider-admin rights. The migrator needs only the reviewed migration privileges. Prove this with negative privilege tests against staging; parsing distinct usernames does not prove correct grants.
- Reserve numeric UID/GID `10001` on the dedicated host and verify they are not assigned to another workload/user. Identify the actual Docker socket GID; do not guess it or make the socket world-accessible.
- A dedicated, quota/bounded-filesystem workspace directory **outside all project checkouts**, for example `/srv/yexjudge/workspaces`, owned `10001:10001`, mode `0700`. Its absolute path is mounted at the **identical path** inside the judge and used for `TMPDIR`/`HOST_WORKSPACE_ROOT`. Ancestors must not be writable by other users. Never bind the repository, `/`, `/home`, or a general-purpose host directory into the judge.
- Protected host secret store files, real TLS certificate chain/private key for every configured DNS name, all seven reviewed image digests installed locally, Docker's built-in seccomp, and working CPU/memory/swap/PID cgroups. No runtime image pull/build is performed by the judge or verifier.
- Host/cloud ingress and egress firewall rules, administrative identity/access policy, disk quotas, backup/restore evidence, monitoring, and incident ownership. These are not configured by Compose.

Rootless Docker is **not a supported promise of this manifest**: socket path, same-path bind mounts, UID mapping, source-file traversal, and resource enforcement need a separate assessment and regression run. The verifier rejects a rootless engine for this deployment. A Docker API proxy is likewise not assumed to provide a sufficient boundary. Separating authenticated API/queue services from Docker-controlling workers, and evaluating a stronger VM/microVM execution boundary, remain future architectural work.

## Code safeguards versus deployment gates

The execution/persistence changes inspected for this packaging provide:

- Fresh compiler containers with read-only `/source`; compilation happens in bounded tmpfs, not a host-writable artifact bind. Compiler descendants are killed before export. The host imports only permitted regular artifacts with bounded file count/bytes and rejects links, special files, traversal, duplicate entries, and unexpected archive metadata. Runtime staging is independently validated; compiled source is not staged into the runtime.
- Both container types use `--pull never`, network `none`, private IPC/cgroup namespaces, dropped capabilities, `no-new-privileges`, read-only roots, no devices/extra mounts, disabled Docker output logging, CPU/memory/swap/PID/file-size/FD/core limits, and non-root identities. Runtime state/processes are cleared by restarting/replacing sandboxes. Compiler users follow the service UID/GID; runtime users are `10001:10001`.
- Bounded stdout/stderr, job/metadata/generated-source/workspace/artifact sizes, aggregate requested testcase time, and cancellation/reaping of local Docker CLI process groups.
- PostgreSQL transactional admission with an advisory lock; owner-scoped lookup/deletion; terminal-only retention using `updated_at`; bounded database operations; attempt-fenced leases/recovery. Active deletion is intentionally rejected. Existing unowned rows are inaccessible through owner-scoped access, not automatically assigned to a new owner.
- A separate `cmd/migrate` using its own `DATABASE_URL_FILE`, bounded connection/statement/lock times, verified production TLS (including configured fallbacks), serialized migrations, and post-migration schema checking.

The **server integration contract** is maintained by the server changes, not implemented by the Docker files. A release must prove that it enforces the contract in the next section; merely setting environment variables does not implement authentication, quotas, retention scheduling, redaction, TLS validation, or schema checks. Run server/security and real PostgreSQL integration tests as part of the release. `--config-only` cannot detect a binary that ignores these settings; live smoke is required and still is not a full security audit.

## Production configuration contract

The standalone manifest fixes `APP_ENV=production`, `ALLOW_INSECURE_LOCAL=false`, `AUTO_MIGRATE=false`, `LISTEN_HOST=0.0.0.0`, and `PORT=8080`. Outside this private-network container the server's default listen host is `127.0.0.1`. Insecure local mode is an explicit development-only opt-in; it is not a production fallback.

`AUTH_CREDENTIALS_FILE=/run/secrets/service_credentials` contains a JSON array. Each entry has **only** `service`, `token`, and `role`. For example, the shape is:

```json
[
  {"service": "yexcode", "token": "<independently generated 32-byte-or-longer secret>", "role": "submit"},
  {"service": "ops", "token": "<different independently generated secret>", "role": "admin"}
]
```

Those strings are documentation placeholders, **not credentials to use**. Generate at least 32 random bytes per token in the protected secret store (64-character random hex is convenient). Length validation cannot prove entropy. Never put a bearer token in a URL, environment variable, CLI argument, checked-in file, shell history, browser bundle, or deployment transcript. Multiple tokens may share a stable service during rotation; keep their role identical and do not reuse a token across identities.

After bearer authentication, submissions creation (including synchronous/legacy create routes), `GET`, and `DELETE` require `X-Yex-User-ID`: **1–128 ASCII characters from `[A-Za-z0-9_.-]`**. YexCode/gateway must derive this stable opaque ID from its authenticated account and overwrite/reject any browser-supplied value. The bearer token identifies the service, not the end user. Ownership is the pair **service + user**, so token rotation must preserve the service name. An ID is not a capability to another user's result; foreign owners receive not-found behavior. Do not grant browsers direct access or admin credentials.

- `/health`: minimally public process liveness only.
- `/ready`, `/diagnostics`: application admin role only, and additionally blocked on public TLS by nginx. The admin listener is host loopback-only `9443`; use authorized SSH tunneling/host monitoring with independent administrative credentials.
- `ALLOWED_HOSTS`: comma-separated exact DNS authorities, including `:9443` for the administrative authority (and `:443` only if a client explicitly sends that port). No wildcards, URL schemes, IP literals, trailing dots, or whitespace. Unknown SNI is rejected; a recognized SNI with unknown HTTP Host is dropped. Both nginx and the application must validate hosts.
- Any browser `Origin` is rejected, not reflected; no browser CORS allowlist is enabled. Gateway-to-judge requests must omit `Origin`.
- `DATABASE_URL_FILE=/run/secrets/database_url`: application URL. The one-shot migrator instead mounts `/run/secrets/migration_database_url`; it receives **no** application token, workspace, or socket.
- Both database secret files must use a URL of the form `postgres://ROLE:URL_ENCODED_PASSWORD@private-db.example/DB?sslmode=verify-full&sslrootcert=/run/secrets/postgres_ca`. Supply real values through the secret store. The verifier intentionally accepts a narrower URL contract than pgx (no keyword DSNs, host overrides, or multi-host fallback query parameters). The application must independently validate actual pgx TLS configuration.
- `RUNTIME_IMAGE`, `C_COMPILER_IMAGE`, `CPP_COMPILER_IMAGE`, `GO_COMPILER_IMAGE`, `JAVA_COMPILER_IMAGE`: reviewed `@sha256:<64 hex>` references required in production. `SERVER_IMAGE` and `PROXY_IMAGE` must also be digest-pinned; no manifest builds or mutable-tag fallback.

Fixed initial limits:

| Setting | Value |
| --- | ---: |
| `MAX_QUEUED` | 100 |
| `MAX_ACTIVE_PER_SERVICE` / `MAX_ACTIVE_PER_USER` | 32 / 2 |
| `SERVICE_REQUESTS_PER_MINUTE` / `USER_REQUESTS_PER_MINUTE` | 600 / 120 |
| `SERVICE_SUBMISSIONS_PER_MINUTE` / `USER_SUBMISSIONS_PER_MINUTE` | 60 / 10 |
| `HTTP_MAX_IN_FLIGHT` | 64 |
| `DB_MAX_OPEN_CONNS` | 16 |
| `RETENTION_DAYS` | 7 |
| `WORKER_COUNT` / `SANDBOX_POOL_SIZE` / `COMPILE_SLOTS` | 4 / 4 / 2 |
| `SUBMIT_TIMEOUT_MS` | 10000 |

`EXPOSE_RESULT_DETAILS=false` is fixed. `ACCEPT_SUBMISSIONS` is an explicit operational switch; the example leaves it **false** until staging/launch gates are satisfied. It stops new admissions, **not existing workers/jobs**. Keep the single-judge topology until multi-instance request-rate coordination has been reviewed; database admission enforcement does not imply globally coordinated HTTP rate limits.

nginx accepts only TLS 1.2/1.3, bounds bodies at 1 MiB, headers at two 8 KiB large buffers, 256 worker connections (upstream connections count too), 16 active requests per address, and 64 per server. Header/body/connect/upstream-read timeouts are 5/10/3/30 seconds; synchronous app waiting is 10 seconds, not an unbounded compile wait. Administrative upstream reads are 10 seconds. Security headers include HSTS, `nosniff`, frame denial, restrictive CSP, no-referrer, and no-store. Go read-header/read/write/idle timeouts must also be implemented and validated in the server; nginx is not their substitute. These inactivity timeouts and connection limits are not complete slow-client/DoS protection; retain host/gateway controls and load tests. Proxy retries are disabled to avoid duplicate submission POSTs.

## Build and provision a reviewed release

1. Review execution, HTTP middleware, logging/redaction, schema/migrations, and dependencies. Run unit/security tests. Build from the exact reviewed commit, not a dirty concurrent checkout.
2. Build `docker/server/Dockerfile` with explicit reviewed digest build arguments `GO_IMAGE` (Go 1.25-compatible Bookworm builder) and `SERVER_BASE_IMAGE` (Alpine `docker:cli`-compatible base). It packages both `/usr/local/bin/yexjudge` and `/usr/local/bin/yexjudge-migrate`, curl/jq for a credential-safe readiness probe, and defaults to `10001:10001`. Build `docker/runtime/Dockerfile` with a reviewed Bookworm digest in `RUNTIME_BASE_IMAGE`. Defaults in these Dockerfiles are **development tags**, not release pins. Package repositories are mutable: capture an SBOM/provenance and pin/review package versions or a repository snapshot in a reproducible release pipeline.
3. Dockerfile-specific `.dockerignore` files prevent sending checkout secrets/workspaces as release build context. The server copies only Go/module/application/schema/probe inputs; the runtime needs no checkout files. Never build with an unsupported Docker version that ignores these context filters.
4. Select a reviewed Alpine nginx image with `/bin/sh`, `awk`, `envsubst`, `wget`, and `nginx` available. The pinned proxy digest may be an approved derivative. Its upstream entrypoint is intentionally bypassed; the mounted, reviewed `docker/nginx/entrypoint.sh` renders a strict host map into bounded tmpfs and runs `nginx -t` before launch. Test this exact image/config/certificate combination in staging.
5. Publish/scan/sign the server/runtime images, review all compiler/proxy/base images, record registry digests, then pre-pull the exact references on the dedicated host through an approved registry path. Compiler images must provide the existing language toolchain and shell/coreutils/tar contract; images declaring `VOLUME`s are rejected. Re-test compiler/runtime ABI compatibility; Go/Java compiler and runtime versions must agree with the release.
6. Create a protected deployment config based on `.env.production.example`, for example `/etc/yexjudge/deployment.env`, and fill every blank required field. Values are literal `KEY=value` (no sourcing, quotes, shell interpolation, or secrets). Store secret files outside the checkout in a root-owned directory such as `/etc/yexjudge/secrets`, mode `0750`, group `10001`; use single-link regular files owned `root:10001`, mode `0640` or `0440`. Do not use symlinks or writable ancestors. Compose's local file secrets are bind mounts, **not encrypted secret storage**, and its `uid`/`gid` remapping cannot be relied on; provision host permissions yourself.
7. Bound workspace filesystem usage with a dedicated filesystem/quota and secure orphan cleanup. Default sandbox/compiler capacity reserves about 3 GiB before server/host overhead; the judge's 1 GiB Compose limit does **not** cover sibling containers. Measure peak compiler/tmpfs/host load and leave headroom (an initial 8 GiB dedicated host is a starting point, not a capacity proof).

The new image runs unprivileged by default. Old development Compose relies on implicit socket/root access and checkout mounts; it is not silently changed here. Developers need explicit local-only identity/socket-group setup and the server's development opt-in; do not relax the production image/manifest to preserve unsafe old defaults.

## Review migrations, verify, and start

**Always use only `docker-compose.production.yml`. Never pass the development file first as an override base.** Merging can preserve development ports, project mounts, database passwords, and settings. Do not rely on an ambient `COMPOSE_FILE` or `.env`.

Run from the YexJudge root (commands intentionally specify the deployment file):

```sh
python3 scripts/verify-production.py --env-file /etc/yexjudge/deployment.env --config-only
```

This validates manifest syntax/topology/settings only, without contacting Docker or reading secret contents. Missing deployment config, blank example entries, tag-only images, unexpected services/mounts/ports, or unsafe settings fail. It prints **NOT CHECKED**, not a launch approval.

Before migrations, disable admission, stop judge/proxy, review each schema change and compatibility window, capture an encrypted backup/PITR checkpoint, and prove a restore on an isolated database. Record reviewed image/schema versions and application grants. Then run only the one-shot migration profile:

```sh
docker compose --env-file /etc/yexjudge/deployment.env -f docker-compose.production.yml stop proxy judge
docker compose --env-file /etc/yexjudge/deployment.env -f docker-compose.production.yml --profile migration run --rm --no-deps migrate
```

Do not automatically apply migrations on application restart. The production application must use `schema.Check` and refuse a missing/out-of-date schema with `AUTO_MIGRATE=false`. The migration command runs `schema.Apply` and `schema.Check` under separate credentials. Review rollback beforehand: code rollback is safe only with a compatible schema; do not assume down migrations exist. Otherwise stop services, restore to a separately prepared database/checkpoint, repoint protected URL files, verify schema/grants, and smoke-test before resuming. Never test restoration against the live production database.

Run prerequisite validation with a host identity authorized to read the protected store and use the local socket:

```sh
python3 scripts/verify-production.py --env-file /etc/yexjudge/deployment.env
```

Then, on an **isolated staging host**, deliberately enable `ACCEPT_SUBMISSIONS=true`, and start the reviewed digest images:

```sh
docker compose --env-file /etc/yexjudge/deployment.env -f docker-compose.production.yml up -d judge proxy
python3 scripts/verify-production.py --env-file /etc/yexjudge/deployment.env --docker-tests --smoke --url https://judge.example.com --admin-url https://judge.example.com:9443 --connect-address 127.0.0.1
```

Replace the example authorities with the configured certificate DNS name. `--connect-address` chooses the socket destination while still validating that DNS name in TLS; it supports host-loopback/SSH-tunnel testing without `--insecure`. For a private staging CA add `--tls-ca /absolute/protected/staging-ca.pem`; certificate verification is never disabled. Do not post real secrets/transcripts to a public CI log.

- Docker attack tests are **opt-in**. They intentionally attempt filesystem/privilege/network access, process/FD/disk/memory exhaustion, output floods, timeouts, artifact attacks, post-reset state checks, and language/C++ driver compatibility. They require root or UID `10001` on dedicated staging, a locally available Go toolchain/module cache, local reviewed images, Linux resource limits, and built-in seccomp. The script runs all four `TestDockerSecurity*` suites with digest overrides, `--pull never`, and no dependency network downloads; **any skip, missing suite, timeout, or failure fails the gate**. They are containment regressions, not escape-proof evidence.
- Endpoint smoke is **opt-in and mutating**: it creates an intentionally wrong-answer Python fixture, verifies TLS/security headers/unknown hosts/Origin rejection, unauthenticated denial across all create routes, 1 MiB body rejection, required/bounded user IDs, cross-user non-disclosure, public/admin isolation, redacted results, terminal deletion, and not-found after deletion. It reads tokens only from the protected file, never CLI/environment, and prints no bodies, tokens, database URLs, or tool errors. A failed/active fixture may remain until terminal retention; no active job is force-deleted.
- Add a temporary second **submit service** to staging to exercise cross-service ownership. Add an overlapping token for the first service to exercise rotation retrieval. Without these, the script explicitly reports those cases **not exercised**; they remain release gates, not successful tests. Remove temporary credentials after testing.
- Run real PostgreSQL migration/admission/lease/ownership/deletion/retention concurrency and privilege integration tests separately against a **disposable staging database**. They are not run against production or with the production credential by this script. Also run server middleware, redaction, request-size, rate-limit/429, bounded concurrency, slow-client, graceful restart/recovery, and failure/lease-fencing tests. Smoke alone does not exhaust every configured limit.

The judge healthcheck calls authenticated `/ready` using admin JSON credentials via stdin curl configuration, not bearer arguments. nginx's healthcheck is loopback process liveness only. nginx waits for initial judge readiness. `restart: unless-stopped`, init/reaping, application SIGTERM handling, 30-second Compose grace, and nginx SIGQUIT support supervision/graceful shutdown. **Compose does not automatically restart unhealthy containers** or remove orphan sibling execution containers. Alert and have an operator/supervisor reconcile dependency failures and orphan cleanup; measure shutdown deadlines in staging.

## Operational security runbooks

### Host firewall and administrative isolation

- Permit public `443` only from the intended gateway/allowlisted service ingress where possible; there is no cleartext port `80`, public judge `8080`, or database listener in this manifest. Keep `9443` loopback-only and SSH restricted to management networks, keys/MFA, named operators, and audited sessions.
- Review Docker's actual packet-filtering/NAT rules, cloud firewall, IPv4/IPv6 paths, and Docker published-port behavior (host UFW alone may not constrain Docker forwarding). Test from an unauthorized machine that `8080`, `9443`, database ports, and Docker TCP `2375/2376` are unreachable. Never expose the Docker daemon on TCP.
- Allow only reviewed outbound database DNS/IP/port and approved operational endpoints; do not interpret a bridge called `database_egress` as an egress firewall. Apply managed database allowlists/TLS and restrict cloud/metadata endpoints. Compilation/execution retain network `none`.
- Admin bearer secrets, SSH access, provider control-plane credentials, and user service tokens must be independent. Monitor auth failures without logging tokens or user identities. Secure host paths, backups, and Docker group membership.

### Retention, deletion, encrypted backups, restoration

- Initial terminal source/result retention is seven days, measured from `updated_at` for `finished`/`failed` rows. Active rows are not purged. Verify the server's cleanup scheduler, batching, DB grants and age/count telemetry actually remove rows. Store methods alone do not schedule cleanup.
- Authenticated `DELETE` is owner service/user scoped and terminal-only; active rows return a conflict. Coordinate user deletion requests with active-job completion and document when deletion becomes effective.
- Database deletion does **not** remove data from WAL, replicas, snapshots, provider logs, exported results, or existing backups. Define separate backup/PITR expiration and legal-hold procedures; document this to clients and reapply deletion records after a restore. Avoid copying production source/hidden data into development.
- Encrypt backups in transit/at rest with separately controlled keys; restrict and audit access. Test scheduled restores into an isolated managed TLS database, validate schema/row counts/owner boundaries/leases, keep workers stopped during restore, and verify application privileges and deletion/retention afterward. Record measured RPO/RTO and provider rotation/recovery steps.
- Alert on disk pressure, retained row age/count, DB growth, WAL/backups and orphan workspace directories. Inspect/clean stale workspaces only under an operator-reviewed process while matching jobs are stopped; never delete arbitrary paths from submission input.

### Token/database/certificate rotation and revocation

1. Generate a new independent token in the protected store. Add it beside the old token with the **same service name and role**. Keep user IDs stable; changing either ownership component makes existing rows inaccessible.
2. Atomically replace the host JSON file with protected ownership/mode, validate, and **recreate** judge (`docker compose ... up -d --force-recreate judge`). Authentication is loaded at process start; local secret bind mounts may still reference an old inode after atomic replacement. A plain process/container restart is not sufficient evidence that a replaced file was rebound.
3. Move the gateway to the new token, prove owner access with the new token, remove the old token, recreate judge again, and prove the old token now gets `401`. Keep overlap short and controlled; never rename the service to rotate a token.
4. For compromise, skip overlap for the compromised token, stop ingress/admissions while recreating/revoking, revoke corresponding gateway credentials, investigate access, and rotate all exposed secrets. A compromised Docker-controlling judge can expose more than its bearer tokens.
5. Rotate application and migration DB credentials independently through the provider, recreate the processes to load the new protected URL files, and revoke old credentials only after verification. For TLS renewal atomically replace key/chain files and recreate nginx to remount them; verify chain/name/expiry and no insecure fallback. Alert ahead of expiration.

Commands abbreviated with `...` above mean the **same explicit env-file and standalone production file flags**, never default Compose discovery. Recreate may cancel active jobs; drain or stop admission and test attempt-fenced recovery. Rotation runbooks need staging evidence before launch.

### Hidden-test-safe logging and monitoring

- Keep `EXPOSE_RESULT_DETAILS=false`. Do not log source, test input/expected/actual output, compiler diagnostics by default, credentials, Authorization, opaque user IDs, URLs with secrets, request bodies, or database driver connection errors. nginx uses only status/timing/upstream status (not path/query/header/body). Review application/provider logging and CI artifacts as separate gates; this manifest cannot redact arbitrary code or third-party logs.
- Restrict aggregate diagnostics to the admin plane. Use approved internal monitors for readiness, queue depth/oldest age, worker/compile/sandbox utilization, lease renewal/recovery, retries/infrastructure errors, Docker operations/replacements, output-limit events, auth/rate-limit failures, DB connections/latency, host CPU/memory/swap/PIDs/disk, orphan execution containers/workspaces, retention lag, TLS expiry, and backup/restore health.
- Alert on saturation, repeated sandbox replacements/lease loss, unexpected successful authentication, leaked/detail-bearing responses, credential changes, public admin reachability, stale retention, and repeated OOM/disk events. Bound/rotate local container logs; ship only reviewed redacted records to restricted storage with its own retention/encryption policy.

### Emergency stop and incident response

- For ordinary overload, set `ACCEPT_SUBMISSIONS=false` and recreate judge with the explicit manifest; reject admission while existing jobs settle. Apply gateway service/IP blocks and notify operators. This is **not** an execution kill switch.
- For suspected escape, secret compromise, or unsafe execution, immediately block gateway/host ingress and stop `proxy` and `judge` with the standalone manifest. Inspect Docker independently: execution siblings may survive a crashed/compromised judge. After preserving necessary evidence, explicitly stop/remove only positively identified `yexjudge-*`/`yexjudge-compile-*` containers on this dedicated daemon. Do not run an unreviewed global container cleanup or assume stopping Compose stops all execution.
- If the Docker-controlling process/host was compromised, treat the entire host and reachable app secrets/data as compromised. Isolate it at the provider/network boundary, revoke service/database/administrative credentials, preserve protected evidence, assess database/backup access, rebuild a patched clean host from reviewed artifacts, and restore verified data. Do not simply restart the old worker and call it remediated.
- Resume only after the incident owner confirms cause/fix, image/host integrity, secret revocation, database consistency, owner isolation, containment/abuse regressions, monitors and retention. Record incident response contacts, escalation times, communication obligations, and an emergency-stop drill before launch.

## Required external release / launch gates (still open until evidenced)

- [ ] Dedicated host isolation, UID/GID/socket permissions, quotas and effective CPU/memory/PID/seccomp enforcement; no unrelated daemon workloads or public Docker listener.
- [ ] Real firewall/admin-plane/egress verification from unauthorized networks, TLS chain/name/renewal, gateway-derived user identity and browser-Origin rejection.
- [ ] Reviewed current server/persistence integration; fail-closed production configuration, verified DB TLS/schema-check behavior and negative privilege tests.
- [ ] Reviewed/signed digest images, SBOM/dependency/image/secret scans, patch schedule and escalation for OS/kernel/Docker/Go/toolchain/base/compiler/proxy vulnerabilities. Rebuild/re-pin/re-test releases; no perpetual pinned-but-unpatched images.
- [ ] Exact-host Docker regressions, server middleware/redaction/abuse/DoS tests, cross-user/cross-service ownership, overlap rotation/revocation, real PostgreSQL concurrency/lease/retention integration, and crash/shutdown/recovery staging evidence. Container-escape risk remains despite passing tests.
- [ ] Source/result/WAL/backup retention and deletion policy, encrypted backups, isolated restore/PITR drill with RPO/RTO, migration review/checkpoint/rollback plan.
- [ ] Restricted hidden-test-safe logs/diagnostics, tested alerts, dependency/unhealthy reconciliation, orphan-worker cleanup, incident owner and emergency-stop/host-rebuild drill.

No script automatically checks off this list. Track evidence for the actual deployed release/host; roadmap Future Work 3 must remain incomplete until those gates are reviewed.
