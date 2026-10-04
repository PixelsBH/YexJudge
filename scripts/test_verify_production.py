"""Offline checks: no Docker daemon, credentials, network, or untrusted execution."""
import copy
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("verify_production", Path(__file__).with_name("verify-production.py"))
assert SPEC is not None and SPEC.loader is not None
verify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verify)


def deployment():
    values = {key: "registry.example/" + key.lower() + "@sha256:" + "a" * 64 for key in verify.IMAGES}
    values.update({key: "/srv/yexjudge-secrets/" + key.lower() for key in verify.FILES})
    values.update({"ALLOWED_HOSTS": "judge.example,judge.example:9443", "TLS_BIND_IP": "127.0.0.1",
                   "HOST_WORKSPACE_ROOT": "/srv/yexjudge/workspaces", "DOCKER_SOCKET_GID": "987",
                   "ACCEPT_SUBMISSIONS": "false"})
    return values


def credential(service="yexcode", role="submit", token="s" * 64):
    return {"service": service, "role": role, "token": token}


class ConfigurationTests(unittest.TestCase):
    def load(self, values, suffix=""):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "deployment.env"
            path.write_text("\n".join(k + "=" + v for k, v in values.items()) + "\n" + suffix)
            return verify.load_env(path)

    def test_explicit_valid_config(self):
        self.assertEqual(self.load(deployment()), deployment())

    def test_example_is_deliberately_not_deployable(self):
        with self.assertRaises(verify.CheckFailed):
            verify.load_env(verify.ROOT / ".env.production.example")

    def test_every_required_setting_fails_closed(self):
        for key in verify.REQUIRED:
            with self.subTest(key=key):
                values = deployment()
                values[key] = ""
                with self.assertRaises(verify.CheckFailed):
                    self.load(values)

    def test_rejects_unpinned_images(self):
        for key in verify.IMAGES:
            with self.subTest(key=key):
                values = deployment()
                values[key] = "image:latest"
                with self.assertRaises(verify.CheckFailed):
                    self.load(values)

    def test_exact_dns_hosts(self):
        for value in ("", "*.example.com", "example.com.", "127.0.0.1", "[::1]", "example.com:0",
                      "example.com:65536", "https://example.com", "-bad.example", "a..example", "a b.example"):
            with self.subTest(value=value):
                self.assertFalse(verify.valid_authority(value))
        for value in ("judge.example", "judge.example:9443", "local-host", "X.example:443"):
            self.assertTrue(verify.valid_authority(value))

    def test_config_injection_and_duplicate_settings(self):
        for suffix in ("APP_ENV=development\n", "TLS_BIND_IP=0.0.0.0\n", "TLS_BIND_IP=$(bad)\n"):
            with self.assertRaises(verify.CheckFailed):
                self.load(deployment(), suffix)

    def test_no_checkout_mount_or_broad_parent_workspace(self):
        for path in (str(verify.ROOT), str(verify.ROOT.parent), "/srv", "/", str(verify.ROOT / "workspace")):
            with self.subTest(path=path):
                values = deployment()
                values["HOST_WORKSPACE_ROOT"] = path
                with self.assertRaises(verify.CheckFailed):
                    self.load(values)

    def test_separate_migration_secret(self):
        values = deployment()
        values["MIGRATION_DATABASE_URL_SOURCE"] = values["DATABASE_URL_SOURCE"]
        with self.assertRaises(verify.CheckFailed):
            self.load(values)


class SecretTests(unittest.TestCase):
    def test_rotation_preserves_stable_service(self):
        entries = [credential(), credential(token="t" * 64), credential("ops", "admin", "a" * 64)]
        self.assertEqual(verify.validate_credentials(json.dumps(entries).encode()), entries)

    def test_rejects_short_duplicate_missing_admin_and_mixed_role(self):
        fixtures = [[], {}, [credential(token="short")], [credential()],
                    [credential(), credential("ops", "admin")],
                    [credential(), credential("yexcode", "admin", "a" * 64)],
                    [credential(token="s" * 32 + "\n"), credential("ops", "admin", "a" * 64)]]
        for entries in fixtures:
            with self.subTest(entries_type=type(entries).__name__), self.assertRaises(verify.CheckFailed):
                verify.validate_credentials(json.dumps(entries).encode())

    def test_database_requires_verified_tls_and_explicit_ca(self):
        base = "postgres://app:never-print-me@db.example/judge?"
        good = "sslmode=verify-full&sslrootcert=/run/secrets/postgres_ca"
        self.assertEqual(verify.validate_database((base + good).encode()), ("db.example", 5432, "/judge", "app"))
        encoded = "postgres://%61pp:never-print-me@db.example/%6audge?" + good
        self.assertEqual(verify.validate_database(encoded.encode()), ("db.example", 5432, "/judge", "app"))
        with self.assertRaises(verify.CheckFailed):
            verify.validate_database((base.replace("db.example", "db.example:0") + good).encode())
        for query in ("sslmode=disable", "sslmode=require", "sslmode=verify-ca", good + "&sslmode=disable",
                      good + "&host=attacker.example", good + "&sslrootcert=/tmp/other"):
            with self.subTest(query_type=query.split("=")[0]):
                with self.assertRaises(verify.CheckFailed) as error:
                    verify.validate_database((base + query).encode())
                self.assertNotIn("never-print-me", str(error.exception))


class ManifestTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Compose config is purely local and does not contact the daemon or pull images.
        cls.values = deployment()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "deployment.env"
            path.write_text("\n".join(k + "=" + v for k, v in cls.values.items()))
            result = subprocess.run(verify.compose_command(path) + ["config", "--format", "json"],
                                    capture_output=True, env=verify.safe_environment(), check=True, timeout=20)
            cls.config = json.loads(result.stdout)

    def check(self, config):
        with patch.object(verify, "command", return_value=json.dumps(config).encode()):
            verify.check_manifest(Path("/unused/deployment.env"), self.values)

    def test_standalone_manifest_contract(self):
        self.check(self.config)

    def test_rejects_development_merge(self):
        config = copy.deepcopy(self.config)
        config["services"]["postgres"] = {"image": "postgres:16"}
        with self.assertRaises(verify.CheckFailed):
            self.check(config)

    def test_safety_setting_mutations(self):
        for key in verify.FIXED_ENV:
            with self.subTest(key=key):
                config = copy.deepcopy(self.config)
                config["services"]["judge"]["environment"][key] = "unsafe"
                with self.assertRaises(verify.CheckFailed):
                    self.check(config)

    def test_socket_never_mounted_in_public_proxy(self):
        config = copy.deepcopy(self.config)
        config["services"]["proxy"]["volumes"].append({
            "type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock",
            "read_only": True, "bind": {"create_host_path": False}})
        with self.assertRaises(verify.CheckFailed):
            self.check(config)

    def test_public_admin_port_is_rejected(self):
        config = copy.deepcopy(self.config)
        for port in config["services"]["proxy"]["ports"]:
            if str(port["published"]) == "9443":
                port["host_ip"] = "0.0.0.0"
        with self.assertRaises(verify.CheckFailed):
            self.check(config)

    def test_private_judge_cannot_publish_http(self):
        config = copy.deepcopy(self.config)
        config["services"]["judge"]["ports"] = [{"published": "8080", "target": 8080}]
        with self.assertRaises(verify.CheckFailed):
            self.check(config)

    def test_migrator_cannot_inherit_socket(self):
        config = copy.deepcopy(self.config)
        config["services"]["migrate"]["volumes"] = config["services"]["judge"]["volumes"]
        with self.assertRaises(verify.CheckFailed):
            self.check(config)


class DockerGateTests(unittest.TestCase):
    suites = ("TestDockerSecurityRuntimeAttacks", "TestDockerSecurityCompileAttacks",
              "TestDockerSecurityCppDriverCompatibility", "TestDockerSecurityLanguageCompatibility")

    def events(self, action="pass"):
        return b"\n".join(json.dumps({"Action": action, "Test": name}).encode() for name in self.suites)

    def test_every_suite_must_pass_without_skip(self):
        with patch.object(verify.os, "getuid", return_value=10001), \
                patch.object(verify, "command", return_value=self.events()) as command:
            verify.docker_regressions(deployment())
        env = command.call_args.kwargs["env"]
        self.assertEqual(env["YEXJUDGE_DOCKER_SECURITY"], "1")
        self.assertEqual(env["GOPROXY"], "off")
        self.assertEqual(env["DOCKER_HOST"], "unix:///var/run/docker.sock")
        self.assertEqual(env["TMPDIR"], deployment()["HOST_WORKSPACE_ROOT"])
        for language in ("C", "CPP", "GO", "JAVA"):
            self.assertEqual(env["YEXJUDGE_SECURITY_" + language + "_IMAGE"],
                             deployment()[language + "_COMPILER_IMAGE"])

    def test_skip_or_absent_suite_fails(self):
        for output in (self.events("skip"), b"", self.events().splitlines()[0]):
            with self.subTest(output_size=len(output)), \
                    patch.object(verify.os, "getuid", return_value=10001), \
                    patch.object(verify, "command", return_value=output), self.assertRaises(verify.CheckFailed):
                verify.docker_regressions(deployment())

    def test_remote_context_cannot_override_local_socket(self):
        with patch.dict(verify.os.environ, {"DOCKER_CONTEXT": "remote", "COMPOSE_FILE": "docker-compose.yml",
                                          "SERVER_IMAGE": "unreviewed:latest"}):
            env = verify.safe_environment()
        self.assertNotIn("DOCKER_CONTEXT", env)
        self.assertNotIn("COMPOSE_FILE", env)
        self.assertNotIn("SERVER_IMAGE", env)


class SmokeGateTests(unittest.TestCase):
    def run_smoke(self, fault=None):
        # Simulates the contract to test the checker, NOT a real TLS/host/auth proof.
        entries = [credential(), credential(token="t" * 64), credential("other", "submit", "b" * 64),
                   credential("ops", "admin", "a" * 64)]
        tokens = {"Bearer " + entry["token"]: entry for entry in entries}
        state: dict[str, object] = {"deleted": False}

        class Response:
            def __init__(self, status, body):
                self.status = status
                self.body = json.dumps(body).encode() if status != 204 else b""

            def read(self, limit):
                return self.body[:limit]

            def getheaders(self):
                return [(key, value) for key, value in {
                    "Strict-Transport-Security": "max-age=31536000", "X-Content-Type-Options": "nosniff",
                    "X-Frame-Options": "DENY", "Content-Security-Policy": "default-src 'none'",
                    "Cache-Control": "no-store"}.items()]

        class Connection:
            def __init__(self, host, port, context, address):
                self.port = port

            def request(self, method, path, body, headers):
                self.method, self.path, self.body, self.headers = method, path, body, headers

            def close(self):
                pass

            def getresponse(self):
                headers, path = self.headers, self.path
                if headers["Host"] == "untrusted.invalid":
                    raise verify.http.client.RemoteDisconnected()
                if "Origin" in headers:
                    return Response(403, {})
                if path == "/health":
                    health = {"status": "ok"}
                    if fault == "health_details":
                        health["database"] = "private"
                    return Response(200, health)
                entry = tokens.get(headers.get("Authorization"))
                if path in ("/ready", "/diagnostics"):
                    if self.port != 9443:
                        return Response(200 if fault == "public_admin" else 404, {"status": "ready"})
                    if not entry:
                        return Response(401, {})
                    if entry["role"] != "admin":
                        return Response(403, {})
                    return Response(200, {"status": "ready"})
                if not entry:
                    return Response(401, {})
                if self.body and len(self.body) > 1048576:
                    return Response(413, {})
                user = headers.get("X-Yex-User-ID", "")
                if not verify.IDENTITY.fullmatch(user):
                    return Response(400, {})
                if path == "/submissions":
                    state["owner"] = (entry["service"], user)
                    return Response(202, {"submissionId": "fixture", "status": "queued"})
                if state.get("owner") != (entry["service"], user) or state["deleted"]:
                    return Response(404, {})
                if self.method == "DELETE":
                    state["deleted"] = True
                    return Response(204, {})
                result = {"id": "fixture", "status": "finished", "result": {"status": "wrong_answer"}}
                if fault == "result_details":
                    result["result"]["failedTestCase"] = {"expectedOutput": "protected"}
                return Response(200, result)

        values = deployment()
        values["ACCEPT_SUBMISSIONS"] = "true"
        with patch.object(verify, "DirectHTTPS", Connection), patch.object(verify.ssl, "create_default_context"):
            verify.smoke(values, entries, "https://judge.example", "https://judge.example:9443", None, "127.0.0.1")
        return state

    def test_smoke_exercises_owned_fixture_and_deletes_it(self):
        self.assertTrue(self.run_smoke()["deleted"])

    def test_public_admin_exposure_fails(self):
        with self.assertRaises(verify.CheckFailed):
            self.run_smoke("public_admin")

    def test_nonminimal_health_fails(self):
        with self.assertRaises(verify.CheckFailed):
            self.run_smoke("health_details")

    def test_result_detail_leak_fails(self):
        with self.assertRaises(verify.CheckFailed):
            self.run_smoke("result_details")


class NginxHostGateTests(unittest.TestCase):
    def test_actual_entrypoint_host_parser(self):
        script = (verify.ROOT / "docker/nginx/entrypoint.sh").read_text()
        parser = script.split("| awk -F, '", 1)[1].split("'; then", 1)[0]
        longest = ".".join(("a" * 63, "b" * 63, "c" * 63, "d" * 61))
        for hosts, success in (("judge.example,judge.example:9443", True),
                               (longest + ":9443", True), ("", False),
                               ("judge.example\nother.example", False), ("*.example", False),
                               ("judge.example:0", False), ("judge.example:65536", False),
                               ("127.0.0.1", False), ("judge.example;injection", False)):
            with self.subTest(success=success, size=len(hosts)):
                result = subprocess.run(["awk", "-F,", parser], input=hosts + "\n", text=True,
                                        capture_output=True, check=False, timeout=5)
                self.assertEqual(result.returncode == 0, success)


if __name__ == "__main__":
    unittest.main()
