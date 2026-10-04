#!/usr/bin/env python3
"""Fail-closed deployment checks. Never print credentials, HTTP bodies or tool errors."""
import argparse
import http.client
import ipaddress
import json
import os
import re
import secrets
import socket
import ssl
import stat
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
IMAGES = ("SERVER_IMAGE", "PROXY_IMAGE", "RUNTIME_IMAGE", "C_COMPILER_IMAGE",
          "CPP_COMPILER_IMAGE", "GO_COMPILER_IMAGE", "JAVA_COMPILER_IMAGE")
FILES = ("SERVICE_CREDENTIALS_SOURCE", "DATABASE_URL_SOURCE", "MIGRATION_DATABASE_URL_SOURCE",
         "PG_CA_SOURCE", "TLS_CERT_SOURCE", "TLS_KEY_SOURCE")
REQUIRED = IMAGES + FILES + ("ALLOWED_HOSTS", "HOST_WORKSPACE_ROOT", "DOCKER_SOCKET_GID", "TLS_BIND_IP")
KEYS = set(REQUIRED) | {"ACCEPT_SUBMISSIONS"}
DIGEST = re.compile(r"[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}\Z")
IDENTITY = re.compile(r"[A-Za-z0-9_.-]{1,128}\Z")
FIXED_ENV = {
    "APP_ENV": "production", "ALLOW_INSECURE_LOCAL": "false", "AUTO_MIGRATE": "false",
    "AUTH_CREDENTIALS_FILE": "/run/secrets/service_credentials",
    "DATABASE_URL_FILE": "/run/secrets/database_url", "LISTEN_HOST": "0.0.0.0", "PORT": "8080",
    "MAX_QUEUED": "100", "MAX_ACTIVE_PER_SERVICE": "32", "MAX_ACTIVE_PER_USER": "2",
    "SERVICE_REQUESTS_PER_MINUTE": "600", "USER_REQUESTS_PER_MINUTE": "120",
    "SERVICE_SUBMISSIONS_PER_MINUTE": "60", "USER_SUBMISSIONS_PER_MINUTE": "10",
    "HTTP_MAX_IN_FLIGHT": "64", "RETENTION_DAYS": "7", "EXPOSE_RESULT_DETAILS": "false",
    "DB_MAX_OPEN_CONNS": "16", "SUBMIT_TIMEOUT_MS": "10000",
}


class CheckFailed(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def valid_authority(value):
    parts = value.split(":")
    if len(parts) > 2 or not parts[0] or len(parts[0]) > 253:
        return False
    if len(parts) == 2 and (not parts[1].isascii() or not parts[1].isdigit()
                            or not 1 <= int(parts[1]) <= 65535):
        return False
    host = parts[0]
    if re.fullmatch(r"[0-9.]+", host):
        return False
    return all(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", label)
               for label in host.split("."))


def external_path(value, key, directory=False):
    path = Path(value)
    require(path.is_absolute() and str(path) == value and len(path.parts) >= 3,
            key + " must be a dedicated, normalized absolute path")
    require(not any(c in value for c in ",:\\"), key + " contains an unsafe mount character")
    require(path.resolve() == path, key + " must not contain symlinks")
    project = ROOT.parent
    require(not path.is_relative_to(project) and not (directory and project.is_relative_to(path)),
            key + " must be outside the project checkout")
    return path


def load_env(path):
    try:
        text = path.read_text(encoding="ascii")
    except (OSError, UnicodeError):
        raise CheckFailed("Cannot read deployment config") from None
    values = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        require(separator and key in KEYS and key not in values,
                "Deployment config requires unique supported literal KEY=value entries")
        require(not any(c.isspace() or c in "#$'\"`" for c in value),
                key + " must be literal, without quoting, interpolation or whitespace")
        values[key] = value
    for key in REQUIRED:
        require(values.get(key), key + " is required (no production fallback)")
    for key in IMAGES:
        require(DIGEST.fullmatch(values[key]), key + " requires a reviewed @sha256 digest")
    hosts = values["ALLOWED_HOSTS"].split(",")
    require(len(hosts) == len(set(hosts)) and all(valid_authority(h) for h in hosts),
            "ALLOWED_HOSTS requires unique exact DNS authorities")
    require(any(h.endswith(":9443") for h in hosts), "ALLOWED_HOSTS must include administrative port 9443")
    require(any(":" not in h for h in hosts), "ALLOWED_HOSTS must include the default TLS authority")
    try:
        ipaddress.IPv4Address(values["TLS_BIND_IP"])
    except ValueError:
        raise CheckFailed("TLS_BIND_IP must be an explicit IPv4 bind address") from None
    require(values["DOCKER_SOCKET_GID"].isdigit() and int(values["DOCKER_SOCKET_GID"]) > 0,
            "DOCKER_SOCKET_GID must be a positive numeric socket group")
    values.setdefault("ACCEPT_SUBMISSIONS", "false")
    require(values["ACCEPT_SUBMISSIONS"] in ("true", "false"), "ACCEPT_SUBMISSIONS must be true or false")
    external_path(values["HOST_WORKSPACE_ROOT"], "HOST_WORKSPACE_ROOT", directory=True)
    for key in FILES:
        external_path(values[key], key)
    require(values["DATABASE_URL_SOURCE"] != values["MIGRATION_DATABASE_URL_SOURCE"],
            "Application and migration credentials require separate files")
    return values


def command(args, env=None, timeout=30):
    try:
        result = subprocess.run(args, cwd=ROOT, env=safe_environment() if env is None else env,
                                        capture_output=True, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise CheckFailed("Required command unavailable or timed out; output suppressed") from None
    require(result.returncode == 0, "Verification command failed; potentially sensitive output suppressed")
    return result.stdout


def compose_command(env_path):
    return ["docker", "--host", "unix:///var/run/docker.sock", "compose", "--env-file", str(env_path),
            "-f", str(ROOT / "docker-compose.production.yml"), "--profile", "migration"]


def safe_environment():
    env = {k: v for k, v in os.environ.items()
           if k not in KEYS and not k.startswith("COMPOSE_")
           and k not in ("DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH")}
    env["COMPOSE_DISABLE_ENV_FILE"] = "1"
    return env


def check_manifest(env_path, values):
    output = command(compose_command(env_path) + ["config", "--format", "json"], env=safe_environment())
    config = json.loads(output)
    require(config.get("name") == "yexjudge-production", "Unexpected production project name")
    services = config.get("services", {})
    require(set(services) == {"judge", "proxy", "migrate"}, "Unexpected production services")
    for name, service in services.items():
        require(service.get("user") == "10001:10001" and service.get("read_only") is True,
                name + " requires numeric unprivileged user and read-only root")
        require(service.get("cap_drop") == ["ALL"] and service.get("security_opt") == ["no-new-privileges:true"],
                name + " requires dropped capabilities and no-new-privileges")
        require(not service.get("privileged") and not service.get("build") and not service.get("network_mode")
                and not service.get("devices") and not service.get("pid") and not service.get("ipc"),
                name + " has an unsafe deployment override")
        require(service.get("pull_policy") == "never" and int(service.get("pids_limit", 0)) > 0
                and int(service.get("mem_limit", 0)) > 0 and float(service.get("cpus", 0)) > 0,
                name + " requires preinstalled images and bounded resources")
    judge, proxy, migrate = (services[k] for k in ("judge", "proxy", "migrate"))
    for key, expected in (FIXED_ENV | {"ALLOWED_HOSTS": values["ALLOWED_HOSTS"],
                                      "TMPDIR": values["HOST_WORKSPACE_ROOT"],
                                      "HOST_WORKSPACE_ROOT": values["HOST_WORKSPACE_ROOT"],
                                      "ACCEPT_SUBMISSIONS": values["ACCEPT_SUBMISSIONS"]}).items():
        require(judge["environment"].get(key) == expected, "Unsafe judge setting: " + key)
    require(not judge.get("ports") and not migrate.get("ports"), "Judge and migrator must not publish ports")
    require(judge.get("group_add") == [values["DOCKER_SOCKET_GID"]], "Judge socket group mismatch")
    mounts = judge.get("volumes", [])
    require(len(mounts) == 2 and {(m["source"], m["target"]) for m in mounts} == {
        ("/var/run/docker.sock", "/var/run/docker.sock"),
        (values["HOST_WORKSPACE_ROOT"], values["HOST_WORKSPACE_ROOT"]),
    }, "Judge may bind only its dedicated same-path workspace and local Docker socket")
    for mount in mounts + proxy.get("volumes", []):
        require(mount["type"] == "bind" and mount.get("bind", {}).get("create_host_path") is False,
                "Production binds must not auto-create host paths")
    proxy_mounts = proxy.get("volumes", [])
    require(len(proxy_mounts) == 2 and all(m.get("read_only") is True for m in proxy_mounts)
            and {(m["source"], m["target"]) for m in proxy_mounts} == {
                (str(ROOT / "docker/nginx/nginx.conf.template"), "/etc/yexjudge/nginx.conf.template"),
                (str(ROOT / "docker/nginx/entrypoint.sh"), "/etc/yexjudge/entrypoint.sh"),
            }, "Proxy may mount only its reviewed config files; never Docker socket or checkout directories")
    for name, service in (("judge", judge), ("proxy", proxy)):
        require(service.get("restart") == "unless-stopped" and service.get("healthcheck")
                and service.get("stop_grace_period") == "30s", name + " requires supervision, health and shutdown grace")
    require(proxy.get("stop_signal") == "SIGQUIT", "Proxy requires graceful SIGQUIT shutdown")
    require(not migrate.get("volumes"), "Migrator must not mount workspaces or Docker socket")
    require(set(migrate["environment"]) == {"APP_ENV", "DATABASE_URL_FILE", "HOME"}
            and migrate["environment"]["DATABASE_URL_FILE"] == "/run/secrets/migration_database_url"
            and migrate.get("profiles") == ["migration"] and migrate.get("restart") == "no",
            "Migrator requires separate one-shot credentials and explicit profile")
    require(set(judge["networks"]) == {"api", "database_egress"}
            and set(proxy["networks"]) == {"api", "ingress"}
            and set(migrate["networks"]) == {"database_egress"}
            and config["networks"]["api"].get("internal") is True,
            "Unexpected production network topology")
    ports = {(p.get("host_ip"), str(p["published"]), p["target"]) for p in proxy.get("ports", [])}
    require(ports == {(values["TLS_BIND_IP"], "443", 8443), ("127.0.0.1", "9443", 8444)},
            "Proxy must publish only TLS and loopback-only administrative TLS")
    for name, key in (("judge", "SERVER_IMAGE"), ("migrate", "SERVER_IMAGE"), ("proxy", "PROXY_IMAGE")):
        require(services[name]["image"] == values[key], "Deployment image mismatch")
    for key in IMAGES[2:]:
        require(judge["environment"].get(key) == values[key], "Execution image mismatch: " + key)
    expected_secrets = {
        "judge": {"service_credentials", "database_url", "postgres_ca"},
        "proxy": {"tls_certificate", "tls_key"}, "migrate": {"migration_database_url", "postgres_ca"},
    }
    for name, expected in expected_secrets.items():
        require({s["source"] for s in services[name]["secrets"]} == expected
                and all(s.get("target", s["source"]) in (s["source"], "/run/secrets/" + s["source"])
                                        for s in services[name]["secrets"]),
                name + " has an unexpected secret mount")
    sources = dict(zip(("service_credentials", "database_url", "migration_database_url", "postgres_ca",
                        "tls_certificate", "tls_key"), FILES))
    require(set(config["secrets"]) == set(sources), "Unexpected deployment secrets")
    for name, key in sources.items():
        require(config["secrets"][name]["file"] == values[key], "Secret source mismatch: " + key)


def read_protected(path, key):
    info = path.stat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == 0 and info.st_gid == 10001
            and stat.S_IMODE(info.st_mode) in (0o440, 0o640),
            key + " requires a single-link regular file, root:10001 ownership and mode 0440/0640")
    for parent in path.parents:
        require(parent.stat().st_mode & 0o022 == 0, key + " has a writable parent directory")
    require(path.parent.stat().st_mode & 0o007 == 0, key + " requires a non-public secret directory")
    require(0 < info.st_size <= 65536, key + " is empty or exceeds the bounded secret size")
    return path.read_bytes()


def validate_credentials(contents):
    try:
        credentials = json.loads(contents)
    except (ValueError, UnicodeError):
        raise CheckFailed("Service credential file must be valid JSON") from None
    require(isinstance(credentials, list) and credentials, "Service credentials require a nonempty JSON array")
    tokens, roles = set(), {}
    for entry in credentials:
        require(isinstance(entry, dict) and set(entry) == {"service", "token", "role"},
                "Service credentials require exactly service/token/role")
        service, token, role = (entry[k] for k in ("service", "token", "role"))
        require(isinstance(service, str) and IDENTITY.fullmatch(service), "Invalid stable credential service")
        require(isinstance(token, str) and len(token.encode()) >= 32 and len(token.encode()) <= 4096
                and not any(ord(c) < 33 or ord(c) > 126 for c in token),
                "Bearer tokens require at least 32 ASCII bytes without whitespace/control characters")
        require(role in ("submit", "admin") and token not in tokens, "Invalid role or duplicate bearer token")
        require(service not in roles or roles[service] == role, "Rotation must preserve a service's role")
        tokens.add(token)
        roles[service] = role
    require(set(roles.values()) == {"submit", "admin"}, "Separate submit and admin service credentials required")
    return credentials


def validate_database(contents):
    try:
        parsed = urlsplit(contents.decode("ascii").strip())
        query = parse_qs(parsed.query, keep_blank_values=True)
        require(parsed.scheme in ("postgres", "postgresql") and parsed.username and parsed.password
                and parsed.hostname and valid_authority(parsed.hostname) and parsed.path not in ("", "/")
                and not parsed.fragment, "Database secrets require explicit PostgreSQL URLs with DNS hosts")
        require(query.get("sslmode") == ["verify-full"]
                and query.get("sslrootcert") == ["/run/secrets/postgres_ca"]
                and set(query) <= {"sslmode", "sslrootcert", "connect_timeout", "application_name"}
                and all(len(v) == 1 for v in query.values()),
                "Database URL requires verify-full and mounted CA; overrides/fallbacks are not accepted")
        port = parsed.port if parsed.port is not None else 5432
        require(1 <= port <= 65535, "Database URL requires a valid port")
        return parsed.hostname, port, unquote(parsed.path), unquote(parsed.username or "")
    except (ValueError, UnicodeError):
        raise CheckFailed("Invalid database URL secret; value suppressed") from None


def check_host(values):
    workspace = external_path(values["HOST_WORKSPACE_ROOT"], "HOST_WORKSPACE_ROOT", directory=True)
    info = workspace.stat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == info.st_gid == 10001
            and stat.S_IMODE(info.st_mode) == 0o700, "Workspace requires 10001:10001 ownership and mode 0700")
    for parent in workspace.parents:
        require(parent.stat().st_mode & 0o022 == 0, "Workspace has a writable parent directory")
    data = {key: read_protected(Path(values[key]), key) for key in FILES}
    credentials = validate_credentials(data["SERVICE_CREDENTIALS_SOURCE"])
    application = validate_database(data["DATABASE_URL_SOURCE"])
    migration = validate_database(data["MIGRATION_DATABASE_URL_SOURCE"])
    require(application[:3] == migration[:3] and application[3] != migration[3],
            "Application/migration URLs must target the same database using distinct roles")
    sock = Path("/var/run/docker.sock").stat()
    require(stat.S_ISSOCK(sock.st_mode) and sock.st_gid == int(values["DOCKER_SOCKET_GID"])
            and sock.st_mode & 0o007 == 0, "Local Docker socket type/group/access is unsafe")
    require(not os.environ.get("DOCKER_HOST") or os.environ["DOCKER_HOST"] == "unix:///var/run/docker.sock",
            "Remote Docker daemons are not supported by this same-path deployment")
    docker = ["docker", "--host", "unix:///var/run/docker.sock"]
    engine = json.loads(command(docker + ["info", "--format", "{{json .}}"]))
    require(engine.get("OSType") == "linux" and all(engine.get(k) for k in
            ("MemoryLimit", "SwapLimit", "PidsLimit", "CPUCfsQuota")), "Docker must enforce Linux resource limits")
    require(any("name=seccomp" in s and "profile=builtin" in s for s in engine.get("SecurityOptions", [])),
            "Docker built-in seccomp is required")
    require(not any("name=rootless" in s for s in engine.get("SecurityOptions", [])),
            "Rootless Docker needs a separately assessed manifest and cgroup/UID mapping")
    for key in IMAGES:
        image = json.loads(command(docker + ["image", "inspect", values[key]]))[0]
        require(image.get("Os") == "linux" and not image.get("Config", {}).get("Volumes"),
                key + " must be installed locally, Linux, and without declared volumes")
        if key in ("SERVER_IMAGE", "RUNTIME_IMAGE"):
            require(image["Config"].get("User") == "10001:10001", key + " must default to unprivileged 10001:10001")
    return credentials


def docker_regressions(values):
    require(os.getuid() in (0, 10001), "Run Docker regressions as root or UID 10001 on the dedicated staging host")
    env = {k: v for k, v in safe_environment().items()
           if not k.startswith("YEXJUDGE_") and not k.startswith("PG")}
    env.update({"YEXJUDGE_DOCKER_SECURITY": "1", "DOCKER_HOST": "unix:///var/run/docker.sock",
                "TMPDIR": values["HOST_WORKSPACE_ROOT"], "GOPROXY": "off", "GOSUMDB": "off",
                "GOTOOLCHAIN": "local", "YEXJUDGE_SECURITY_RUNTIME_IMAGE": values["RUNTIME_IMAGE"]})
    for language in ("C", "CPP", "GO", "JAVA"):
        env["YEXJUDGE_SECURITY_" + language + "_IMAGE"] = values[language + "_COMPILER_IMAGE"]
    output = command(["go", "test", "-json", "./internal/judge", "-run", "^TestDockerSecurity",
                      "-count=1", "-timeout=5m"], env=env, timeout=360)
    events = [json.loads(line) for line in output.splitlines()]
    require(not any(e.get("Action") == "skip" for e in events), "Docker regression tests must not skip")
    passed = {e.get("Test") for e in events if e.get("Action") == "pass"}
    expected = {"TestDockerSecurityRuntimeAttacks", "TestDockerSecurityCompileAttacks",
                "TestDockerSecurityCppDriverCompatibility", "TestDockerSecurityLanguageCompatibility"}
    require(expected <= passed, "All four Docker security regression suites must actually pass")


class DirectHTTPS(http.client.HTTPSConnection):
    def __init__(self, host, port, context, address):
        super().__init__(host, port, timeout=10, context=context)
        self.address = address or host
        self.tls_context = context

    def connect(self):
        sock = socket.create_connection((self.address, self.port), self.timeout)
        try:
            self.sock = self.tls_context.wrap_socket(sock, server_hostname=self.host)
        except Exception:
            sock.close()
            raise


def smoke(values, credentials, url, admin_url, ca_file, address):
    require(values["ACCEPT_SUBMISSIONS"] == "true", "Endpoint smoke requires admissions enabled on isolated staging")
    urls = [urlsplit(u) for u in (url, admin_url)]
    for parsed in urls:
        require(parsed.scheme == "https" and parsed.hostname and not parsed.username
                and not parsed.password and parsed.path in ("", "/") and not parsed.query and not parsed.fragment,
                "Smoke endpoints must be HTTPS authorities, not paths or credential-bearing URLs")
        require(parsed.netloc in values["ALLOWED_HOSTS"].split(","), "Smoke authorities must be explicitly allowed")
    require((urls[0].port or 443) == 443 and urls[1].port == 9443,
            "Smoke requires public TLS 443 and isolated administrative TLS 9443")
    context = ssl.create_default_context(cafile=ca_file)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    submitters = [c for c in credentials if c["role"] == "submit"]
    submitter = submitters[0]
    admin = next(c for c in credentials if c["role"] == "admin")
    user = "security-smoke-" + secrets.token_hex(12)
    hidden = "hidden-smoke-" + secrets.token_hex(16)
    forbidden = {"sourceCode", "testCases", "job", "failedTestCase", "errorMessage", "failureMessage",
                 "expectedOutput", "actualOutput", "ownerService", "ownerUser"}

    def no_details(value):
        if isinstance(value, dict):
            require(not (forbidden & value.keys()), "Endpoint exposed protected submission/result details")
            for child in value.values():
                no_details(child)
        elif isinstance(value, list):
            for child in value:
                no_details(child)

    def request(method, path, expected, credential=None, identity=None, body=None, administrative=False, extra=None):
        parsed = urls[int(administrative)]
        headers = {"Host": parsed.netloc, "Content-Type": "application/json"}
        if credential:
            headers["Authorization"] = "Bearer " + credential["token"]
        if identity is not None:
            headers["X-Yex-User-ID"] = identity
        headers.update(extra or {})
        payload = json.dumps(body).encode() if body is not None else None
        connection = DirectHTTPS(parsed.hostname, parsed.port or 443, context, address)
        try:
            connection.request(method, path, body=payload, headers=headers)
            response = connection.getresponse()
            data = response.read(1048577)
            status, response_headers = response.status, dict(response.getheaders())
        except http.client.RemoteDisconnected:
            status, data, response_headers = 0, b"", {}
        finally:
            connection.close()
        require(status in expected, "Endpoint smoke failed: " + method + " " + path.split("/")[1] + " status gate")
        require(len(data) <= 1048576 and hidden.encode() not in data, "Endpoint body bound/redaction failed")
        try:
            document = json.loads(data) if data else {}
        except ValueError:
            document = {}
        if status in (200, 202):
            require(isinstance(document, dict) and document, "Successful API response must be a JSON object")
        return document, {k.lower(): v for k, v in response_headers.items()}

    health, headers = request("GET", "/health", {200})
    require(isinstance(health, dict) and set(health) == {"status"}, "Public health must remain minimal")
    require(all(h in headers for h in ("strict-transport-security", "x-content-type-options",
                                       "x-frame-options", "content-security-policy", "cache-control")),
            "TLS security headers missing")
    request("GET", "/health", {0}, extra={"Host": "untrusted.invalid"})
    request("GET", "/health", {403}, extra={"Origin": "https://browser.invalid"})
    for path in ("/ready", "/diagnostics"):
        request("GET", path, {404}, credential=admin)
        request("GET", path, {401}, administrative=True)
        request("GET", path, {403}, credential=submitter, administrative=True)
        doc, _ = request("GET", path, {200}, credential=admin, administrative=True)
        no_details(doc)
    job = {"language": "python", "sourceCode": "print('visible-smoke-output')\n",
           "testCases": [{"input": "", "expectedOutput": hidden}],
           "limits": {"timeLimitMs": 1000, "memoryLimitMb": 128}}
    for route in ("/submissions", "/judge", "/submit"):
        request("POST", route, {401}, identity=user, body=job)
    request("POST", "/submissions", {413}, credential=submitter, identity=user,
            body={"sourceCode": "x" * 1048576})
    request("GET", "/submissions/nonexistent", {401}, identity=user)
    request("DELETE", "/submissions/nonexistent", {401}, identity=user)
    request("POST", "/submissions", {400}, credential=submitter, body=job)
    for identity in ("invalid!", "x" * 129):
        request("POST", "/submissions", {400}, credential=submitter, identity=identity, body=job)
    accepted, _ = request("POST", "/submissions", {202}, credential=submitter, identity=user, body=job)
    identifier = accepted.get("submissionId", "")
    require(isinstance(identifier, str) and IDENTITY.fullmatch(identifier), "Invalid submission acceptance contract")
    path = "/submissions/" + identifier
    try:
        request("GET", path, {400}, credential=submitter)
        request("DELETE", path, {400}, credential=submitter)
        request("GET", path, {404}, credential=submitter, identity=user + "-other")
        request("DELETE", path, {404}, credential=submitter, identity=user + "-other")
        for other in submitters[1:]:
            expected = {200} if other["service"] == submitter["service"] else {404}
            request("GET", path, expected, credential=other, identity=user)
            if other["service"] != submitter["service"]:
                request("DELETE", path, {404}, credential=other, identity=user)
        deadline = time.monotonic() + 60
        while True:
            result, _ = request("GET", path, {200}, credential=submitter, identity=user)
            no_details(result)
            if result.get("status") in ("finished", "failed"):
                require(result.get("result", {}).get("status") == "wrong_answer", "Smoke job execution did not reach expected verdict")
                break
            require(time.monotonic() < deadline, "Smoke submission did not finish within 60 seconds")
            time.sleep(1)
        request("DELETE", path, {204}, credential=submitter, identity=user)
        request("GET", path, {404}, credential=submitter, identity=user)
    finally:
        # Best effort for a terminal fixture; active rows are intentionally not force-deleted.
        try:
            request("DELETE", path, {204, 404, 409}, credential=submitter, identity=user)
        except (CheckFailed, OSError, http.client.HTTPException):
            pass
    if not any(c["service"] != submitter["service"] for c in submitters):
        print("NOTE: cross-service smoke needs a second temporary submit service; not exercised")
    if not any(c["service"] == submitter["service"] for c in submitters[1:]):
        print("NOTE: overlapping-token smoke needs a second token for the same service; not exercised")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--config-only", action="store_true", help="Syntax/configuration only; NOT a host or launch gate")
    parser.add_argument("--docker-tests", action="store_true", help="Run attack regressions on dedicated staging (CPU/memory/PID stress)")
    parser.add_argument("--smoke", action="store_true", help="Exercise HTTPS auth, ownership, redaction and deletion; creates a fixture")
    parser.add_argument("--url")
    parser.add_argument("--admin-url")
    parser.add_argument("--tls-ca", help="Optional trusted staging TLS CA; never disables certificate verification")
    parser.add_argument("--connect-address", help="Optional local/tunnel destination; TLS verifies the URL DNS hostname")
    args = parser.parse_args()
    try:
        require(not args.config_only or not (args.docker_tests or args.smoke), "Config-only cannot run deployment tests")
        require(not args.smoke or (args.url and args.admin_url), "Smoke requires --url and --admin-url")
        env_path = args.env_file.resolve()
        values = load_env(env_path)
        check_manifest(env_path, values)
        print("PASS: standalone manifest and explicit production configuration")
        if args.config_only:
            print("NOT CHECKED: secrets, host isolation, Docker enforcement, live endpoints or launch gates")
            return 0
        credentials = check_host(values)
        print("PASS: local secret/workspace/socket prerequisites and installed digest images")
        if args.docker_tests:
            docker_regressions(values)
            print("PASS: opt-in Docker containment/compatibility regressions (not escape-proof evidence)")
        if args.smoke:
            smoke(values, credentials, args.url, args.admin_url, args.tls_ca, args.connect_address)
            print("PASS: opt-in TLS/auth/admin isolation/ownership/redaction/delete endpoint smoke")
        if not args.docker_tests or not args.smoke:
            print("NOT CHECKED: one or both opt-in Docker regressions / live endpoint smoke")
        print("NOT CERTIFIED: firewall, quotas, database privileges/backups, patching, restore, monitoring and incident drills")
        return 0
    except (CheckFailed, OSError, ValueError, KeyError, TypeError, AttributeError, http.client.HTTPException):
        # Only our own constant/field-name messages are safe to display; never raw driver/tool/HTTP exceptions.
        error = sys.exc_info()[1]
        message = str(error) if isinstance(error, CheckFailed) else "Prerequisite or verification failed; details suppressed"
        print("FAIL: " + message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
