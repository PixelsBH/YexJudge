package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func securityConfigTestEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"APP_ENV", "ALLOW_INSECURE_LOCAL", "AUTH_CREDENTIALS", "AUTH_CREDENTIALS_FILE", "ALLOWED_HOSTS",
		"MAX_QUEUED", "MAX_ACTIVE_PER_SERVICE", "MAX_ACTIVE_PER_USER", "SERVICE_REQUESTS_PER_MINUTE",
		"USER_REQUESTS_PER_MINUTE", "SERVICE_SUBMISSIONS_PER_MINUTE", "USER_SUBMISSIONS_PER_MINUTE",
		"HTTP_MAX_IN_FLIGHT", "EXPOSE_RESULT_DETAILS", "ACCEPT_SUBMISSIONS", "LISTEN_HOST", "PORT",
		"DATABASE_URL", "DATABASE_URL_FILE", "AUTO_MIGRATE", "DB_MAX_OPEN_CONNS", "RETENTION_DAYS",
		"WORKER_COUNT", "SANDBOX_POOL_SIZE", "COMPILE_SLOTS", "QUEUE_POLL_INTERVAL_MS", "QUEUE_LEASE_MS",
		"QUEUE_RECOVERY_INTERVAL_MS", "QUEUE_MAX_ATTEMPTS", "SUBMIT_TIMEOUT_MS", "RUNTIME_IMAGE",
		"C_COMPILER_IMAGE", "CPP_COMPILER_IMAGE", "GO_COMPILER_IMAGE", "JAVA_COMPILER_IMAGE",
		"DOCKER_HOST", "DOCKER_CONTEXT", "HOST_WORKSPACE_ROOT", "TMPDIR",
		"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE",
		"PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGCONNECT_TIMEOUT", "PGOPTIONS",
	} {
		t.Setenv(key, "")
	}
}

func securityConfigTestCredentials(t *testing.T, credentials ...serviceCredential) string {
	t.Helper()
	if len(credentials) == 0 {
		credentials = []serviceCredential{
			{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"},
			{Service: "operations", Token: securityTestAdminToken, Role: "admin"},
		}
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func securityConfigTestFile(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protected-config")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func securityConfigTestProduction(t *testing.T) {
	t.Helper()
	securityConfigTestEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_CREDENTIALS_FILE", securityConfigTestFile(t, securityConfigTestCredentials(t), 0600))
	t.Setenv("DATABASE_URL_FILE", securityConfigTestFile(t, "postgres://judge:database-test-secret@db.example.test:5432/judge?sslmode=verify-full", 0600))
	t.Setenv("ALLOWED_HOSTS", securityTestHost)
	for _, key := range []string{"RUNTIME_IMAGE", "C_COMPILER_IMAGE", "CPP_COMPILER_IMAGE", "GO_COMPILER_IMAGE", "JAVA_COMPILER_IMAGE"} {
		t.Setenv(key, "reviewed/image@sha256:"+strings.Repeat("a", 64))
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_WORKSPACE_ROOT", root)
	t.Setenv("TMPDIR", root)
}

func TestSecurityConfigCredentialSchemaAndDigests(t *testing.T) {
	document := securityConfigTestCredentials(t,
		serviceCredential{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"},
		serviceCredential{Service: "gateway", Token: securityTestRotatedToken, Role: "submit"},
		serviceCredential{Service: "operations", Token: securityTestAdminToken, Role: "admin"},
	)
	digests, err := parseCredentials(document)
	if err != nil || len(digests) != 3 {
		t.Fatalf("valid rotation document = %+v, error %v", digests, err)
	}
	for index, expected := range []struct{ service, role, token string }{
		{"gateway", "submit", securityTestSubmitToken}, {"gateway", "submit", securityTestRotatedToken}, {"operations", "admin", securityTestAdminToken},
	} {
		if digests[index].service != expected.service || digests[index].role != expected.role || digests[index].digest != sha256.Sum256([]byte(expected.token)) {
			t.Fatalf("credential %d was not stored as the expected digest", index)
		}
	}
	typeOfDigest, _ := json.Marshal(digests)
	if strings.Contains(string(typeOfDigest), securityTestSubmitToken) || strings.Contains(string(typeOfDigest), securityTestAdminToken) {
		t.Fatal("digest configuration retained serializable raw tokens")
	}
	tooMany := make([]serviceCredential, 65)
	for index := range tooMany {
		tooMany[index] = serviceCredential{Service: "gateway", Token: strings.Repeat("t", 32) + strconv.Itoa(index), Role: "submit"}
	}
	for _, test := range []struct{ name, document string }{
		{"empty", ""}, {"null", "null"}, {"empty list", "[]"}, {"object instead of list", `{}`},
		{"unknown field", `[{"service":"gateway","token":"` + securityTestSubmitToken + `","role":"submit","admin":true}]`},
		{"trailing value", document + `{}`}, {"malformed trailing data", document + `garbage`},
		{"missing role", `[{"service":"gateway","token":"` + securityTestSubmitToken + `"}]`},
		{"unknown role", securityConfigTestCredentials(t, serviceCredential{Service: "gateway", Token: securityTestSubmitToken, Role: "superuser"})},
		{"invalid service", securityConfigTestCredentials(t, serviceCredential{Service: "gateway:alice", Token: securityTestSubmitToken, Role: "submit"})},
		{"empty service", securityConfigTestCredentials(t, serviceCredential{Token: securityTestSubmitToken, Role: "submit"})},
		{"short token", securityConfigTestCredentials(t, serviceCredential{Service: "gateway", Token: "short-secret", Role: "submit"})},
		{"long token", securityConfigTestCredentials(t, serviceCredential{Service: "gateway", Token: strings.Repeat("x", 257), Role: "submit"})},
		{"whitespace token", securityConfigTestCredentials(t, serviceCredential{Service: "gateway", Token: securityTestSubmitToken + "\n", Role: "submit"})},
		{"duplicate token", securityConfigTestCredentials(t,
			serviceCredential{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"},
			serviceCredential{Service: "other", Token: securityTestSubmitToken, Role: "submit"})},
		{"role escalation via rotation", securityConfigTestCredentials(t,
			serviceCredential{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"},
			serviceCredential{Service: "gateway", Token: securityTestAdminToken, Role: "admin"})},
		{"too many credentials", securityConfigTestCredentials(t, tooMany...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if credentials, err := parseCredentials(test.document); err == nil {
				t.Fatalf("invalid credential schema accepted: %+v", credentials)
			} else if strings.Contains(err.Error(), securityTestSubmitToken) || strings.Contains(err.Error(), "short-secret") {
				t.Fatal("credential parser error disclosed a token")
			}
		})
	}
	// The documented bound includes 64 credentials, not merely 63.
	if credentials, err := parseCredentials(securityConfigTestCredentials(t, tooMany[:64]...)); err != nil || len(credentials) != 64 {
		t.Fatalf("credential boundary rejected: count=%d error=%v", len(credentials), err)
	}
}

func TestSecurityConfigTokenSyntaxBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, token string
		valid       bool
	}{
		{"minimum", strings.Repeat("a", 32), true}, {"maximum", strings.Repeat("a", 256), true},
		{"printable punctuation", strings.Repeat("!~", 16), true},
		{"short", strings.Repeat("a", 31), false}, {"long", strings.Repeat("a", 257), false},
		{"space", strings.Repeat("a", 32) + " ", false}, {"tab", strings.Repeat("a", 32) + "\t", false},
		{"control", strings.Repeat("a", 32) + "\x00", false}, {"delete", strings.Repeat("a", 32) + "\x7f", false},
		{"unicode", strings.Repeat("a", 32) + "é", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validServiceToken(test.token); got != test.valid {
				t.Fatalf("validServiceToken() = %t, want %t", got, test.valid)
			}
		})
	}
}

func TestSecurityConfigProtectedSecretFiles(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       os.FileMode
		production bool
		allowed    bool
	}{
		{"private", 0600, true, true}, {"private read only", 0400, true, true}, {"group readable", 0640, true, true},
		{"world readable", 0644, true, false}, {"group writable", 0620, true, false}, {"other writable", 0602, true, false},
		{"other executable", 0601, true, false}, {"development compatibility", 0644, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AUTH_CREDENTIALS", "")
			path := securityConfigTestFile(t, " \nprotected-test-secret\n ", test.mode)
			t.Setenv("AUTH_CREDENTIALS_FILE", path)
			value, err := readConfigSecret("AUTH_CREDENTIALS", test.production)
			if test.allowed {
				if err != nil || value != "protected-test-secret" {
					t.Fatalf("protected file = %q, error %v", value, err)
				}
			} else if err == nil {
				t.Fatal("unsafe production file permissions accepted")
			} else if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "protected-test-secret") {
				t.Fatal("protected file error disclosed a path or secret")
			}
		})
	}
	for _, kind := range []string{"symlink", "directory", "missing", "oversized", "empty"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("AUTH_CREDENTIALS", "")
			path := securityConfigTestFile(t, "protected-test-secret", 0600)
			switch kind {
			case "symlink":
				link := filepath.Join(t.TempDir(), "secret-link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "directory":
				path = t.TempDir()
			case "missing":
				path = filepath.Join(t.TempDir(), "missing-secret")
			case "oversized":
				path = securityConfigTestFile(t, strings.Repeat("x", maxConfigSecretBytes+1), 0600)
			case "empty":
				path = securityConfigTestFile(t, " \n\t", 0600)
			}
			t.Setenv("AUTH_CREDENTIALS_FILE", path)
			if value, err := readConfigSecret("AUTH_CREDENTIALS", true); err == nil || value != "" {
				t.Fatalf("unsafe secret file accepted: bytes=%d error=%v", len(value), err)
			} else if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "protected-test-secret") {
				t.Fatal("file failure disclosed protected data")
			}
		})
	}
	t.Run("bounded file boundary", func(t *testing.T) {
		t.Setenv("AUTH_CREDENTIALS", "")
		t.Setenv("AUTH_CREDENTIALS_FILE", securityConfigTestFile(t, strings.Repeat("x", maxConfigSecretBytes), 0600))
		if value, err := readConfigSecret("AUTH_CREDENTIALS", true); err != nil || len(value) != maxConfigSecretBytes {
			t.Fatalf("bounded file boundary rejected: bytes=%d error=%v", len(value), err)
		}
	})
}

func TestSecurityConfigSecretSourcesFailClosed(t *testing.T) {
	for _, key := range []string{"AUTH_CREDENTIALS", "DATABASE_URL"} {
		for _, test := range []struct {
			name       string
			inline     string
			file       bool
			production bool
			allowed    bool
		}{
			{"development inline", "protected-test-secret", false, false, true},
			{"production inline", "protected-test-secret", false, true, false},
			{"ambiguous sources", "protected-test-secret", true, false, false},
			{"production file", "", true, true, true},
		} {
			t.Run(key+"/"+test.name, func(t *testing.T) {
				t.Setenv(key, test.inline)
				t.Setenv(key+"_FILE", "")
				if test.file {
					t.Setenv(key+"_FILE", securityConfigTestFile(t, "protected-test-secret", 0600))
				}
				value, err := readConfigSecret(key, test.production)
				if test.allowed {
					if err != nil || value != "protected-test-secret" {
						t.Fatalf("secret source = %q, error %v", value, err)
					}
				} else if err == nil || value != "" || strings.Contains(err.Error(), "protected-test-secret") {
					t.Fatalf("unsafe secret source or error: value=%q error=%v", value, err)
				}
			})
		}
	}
}

func TestSecurityConfigRequiresExplicitLocalOptInAndProductionRoles(t *testing.T) {
	for _, test := range []struct {
		name, want string
		setup      func(*testing.T)
	}{
		{"no credentials", "AUTH_CREDENTIALS_FILE", func(*testing.T) {}},
		{"unknown environment", "APP_ENV", func(t *testing.T) { t.Setenv("APP_ENV", "staging") }},
		{"production local bypass", "ALLOW_INSECURE_LOCAL", func(t *testing.T) {
			t.Setenv("APP_ENV", "production")
			t.Setenv("ALLOW_INSECURE_LOCAL", "true")
		}},
		{"mixed insecure and authenticated", "cannot be combined", func(t *testing.T) {
			t.Setenv("ALLOW_INSECURE_LOCAL", "true")
			t.Setenv("AUTH_CREDENTIALS", securityConfigTestCredentials(t))
		}},
		{"production missing admin", "separate admin and submit", func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv("AUTH_CREDENTIALS_FILE", securityConfigTestFile(t, securityConfigTestCredentials(t, serviceCredential{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"}), 0600))
		}},
		{"production missing submit", "separate admin and submit", func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv("AUTH_CREDENTIALS_FILE", securityConfigTestFile(t, securityConfigTestCredentials(t, serviceCredential{Service: "operations", Token: securityTestAdminToken, Role: "admin"}), 0600))
		}},
		{"production missing exact hosts", "ALLOWED_HOSTS", func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv("ALLOWED_HOSTS", "")
		}},
		{"production protected details", "EXPOSE_RESULT_DETAILS", func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv("EXPOSE_RESULT_DETAILS", "true")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			securityConfigTestEnvironment(t)
			test.setup(t)
			if _, err := loadSecurityConfig("8080"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadSecurityConfig() error = %v, want %q", err, test.want)
			}
		})
	}
	t.Run("explicit development loopback defaults", func(t *testing.T) {
		securityConfigTestEnvironment(t)
		t.Setenv("ALLOW_INSECURE_LOCAL", "true")
		cfg, err := loadSecurityConfig("8080")
		if err != nil || !cfg.insecureLocal || cfg.production || !cfg.exposeDetails || !cfg.acceptSubmissions || len(cfg.credentials) != 0 {
			t.Fatalf("explicit local configuration = %+v, error %v", cfg, err)
		}
		for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080"} {
			if !cfg.allowedHosts[host] {
				t.Fatalf("local authority missing: %s", host)
			}
		}
	})
	t.Run("authenticated development withholds details", func(t *testing.T) {
		securityConfigTestEnvironment(t)
		t.Setenv("AUTH_CREDENTIALS", securityConfigTestCredentials(t))
		cfg, err := loadSecurityConfig("8080")
		if err != nil || cfg.insecureLocal || cfg.exposeDetails || len(cfg.credentials) != 2 {
			t.Fatalf("authenticated development configuration = %+v, error %v", cfg, err)
		}
	})
}

func TestSecurityConfigStrictBooleansAndBoundedLimits(t *testing.T) {
	for _, key := range []string{"ALLOW_INSECURE_LOCAL", "EXPOSE_RESULT_DETAILS", "ACCEPT_SUBMISSIONS"} {
		for _, value := range []string{"TRUE", "1", "yes", " false "} {
			t.Run(key+"/"+value, func(t *testing.T) {
				securityConfigTestEnvironment(t)
				t.Setenv("AUTH_CREDENTIALS", securityConfigTestCredentials(t))
				t.Setenv(key, value)
				if _, err := loadSecurityConfig("8080"); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("noncanonical boolean accepted: %v", err)
				}
			})
		}
	}
	for _, setting := range []struct {
		key     string
		maximum int
	}{
		{"MAX_QUEUED", 10000}, {"MAX_ACTIVE_PER_SERVICE", 10000}, {"MAX_ACTIVE_PER_USER", 1000},
		{"SERVICE_REQUESTS_PER_MINUTE", 100000}, {"USER_REQUESTS_PER_MINUTE", 10000},
		{"SERVICE_SUBMISSIONS_PER_MINUTE", 10000}, {"USER_SUBMISSIONS_PER_MINUTE", 1000}, {"HTTP_MAX_IN_FLIGHT", 1024},
	} {
		for _, value := range []string{"0", "-1", "not-an-integer", strconv.Itoa(setting.maximum + 1)} {
			t.Run(setting.key+"/"+value, func(t *testing.T) {
				securityConfigTestEnvironment(t)
				t.Setenv("AUTH_CREDENTIALS", securityConfigTestCredentials(t))
				t.Setenv(setting.key, value)
				if _, err := loadSecurityConfig("8080"); err == nil || !strings.Contains(err.Error(), setting.key) {
					t.Fatalf("unbounded setting accepted: %v", err)
				}
			})
		}
	}
	for _, test := range []struct{ key, value string }{{"MAX_ACTIVE_PER_USER", "33"}, {"MAX_ACTIVE_PER_SERVICE", "101"}} {
		t.Run(test.key+"/inconsistent quota", func(t *testing.T) {
			securityConfigTestEnvironment(t)
			t.Setenv("AUTH_CREDENTIALS", securityConfigTestCredentials(t))
			t.Setenv(test.key, test.value)
			if _, err := loadSecurityConfig("8080"); err == nil || !strings.Contains(err.Error(), "must not exceed") {
				t.Fatalf("inconsistent admission quotas accepted: %v", err)
			}
		})
	}
}

func TestSecurityConfigAuthorityValidationAndAllowlist(t *testing.T) {
	for _, test := range []struct {
		authority  string
		production bool
		valid      bool
	}{
		{securityTestHost, true, true}, {"JUDGE.example.test:443", true, true}, {"localhost:8080", false, true},
		{"127.0.0.1:8080", false, true}, {"[::1]:8080", false, true},
		{"127.0.0.1", true, false}, {"[::1]:8080", true, false}, {"::1", false, false},
		{"", true, false}, {"*.example.test", true, false}, {"https://judge.example.test", true, false},
		{"judge.example.test/path", true, false}, {"user@judge.example.test", true, false},
		{"judge.example.test.", true, false}, {"judge..example.test", true, false}, {"-judge.example.test", true, false},
		{"judge-.example.test", true, false}, {" judge.example.test", true, false}, {"judge.example.test\t", true, false},
		{"judge.example.test:0443", true, false}, {"judge.example.test:0", true, false}, {"judge.example.test:65536", true, false},
	} {
		t.Run(test.authority+"/production="+strconv.FormatBool(test.production), func(t *testing.T) {
			if got := validAuthority(test.authority, test.production); got != test.valid {
				t.Fatalf("validAuthority() = %t, want %t", got, test.valid)
			}
		})
	}
	for _, hosts := range []string{"*.example.test", securityTestHost + ", evil.example.test", "127.0.0.1:8080", securityTestHost + ",", "https://" + securityTestHost} {
		t.Run("allowlist/"+hosts, func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv("ALLOWED_HOSTS", hosts)
			if _, err := loadSecurityConfig("8080"); err == nil || !strings.Contains(err.Error(), "ALLOWED_HOSTS") {
				t.Fatalf("invalid production allowlist accepted: %v", err)
			}
		})
	}
}

func TestSecurityConfigLoopbackBindingAndDatabaseRequired(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "localhost", "192.0.2.1"} {
		t.Run(host, func(t *testing.T) {
			securityConfigTestEnvironment(t)
			t.Setenv("ALLOW_INSECURE_LOCAL", "true")
			t.Setenv("DATABASE_URL", "postgres://judge:secret@localhost/judge?sslmode=disable")
			t.Setenv("LISTEN_HOST", host)
			cfg, err := loadConfig()
			if host == "127.0.0.1" || host == "::1" {
				if err != nil || cfg.listenHost != host {
					t.Fatalf("loopback binding rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Fatalf("insecure non-loopback binding accepted: %v", err)
			}
		})
	}
	t.Run("no implicit database", func(t *testing.T) {
		securityConfigTestEnvironment(t)
		t.Setenv("ALLOW_INSECURE_LOCAL", "true")
		if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Fatalf("implicit database configuration accepted: %v", err)
		}
	})
}

func TestSecurityConfigProductionDeploymentAndSchemaMigrationGate(t *testing.T) {
	securityConfigTestProduction(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("valid production configuration rejected: %v", err)
	}
	if !cfg.security.production || cfg.security.insecureLocal || cfg.security.exposeDetails || cfg.autoMigrate || !cfg.security.acceptSubmissions {
		t.Fatal("production default enables an insecure mode, details, or automatic schema migration")
	}
	if cfg.databaseConfig == nil || cfg.databaseConfig.TLSConfig == nil || cfg.databaseConfig.TLSConfig.InsecureSkipVerify || cfg.databaseConfig.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("protected production database config is not verified TLS")
	}
	for _, test := range []struct{ key, value, want string }{
		{"AUTO_MIGRATE", "true", "AUTO_MIGRATE"}, {"AUTO_MIGRATE", "yes", "AUTO_MIGRATE"},
		{"DB_MAX_OPEN_CONNS", "129", "DB_MAX_OPEN_CONNS"}, {"RETENTION_DAYS", "366", "RETENTION_DAYS"},
		{"SUBMIT_TIMEOUT_MS", "15001", "safe limits"}, {"QUEUE_LEASE_MS", "300001", "safe limits"},
		{"QUEUE_MAX_ATTEMPTS", "11", "QUEUE_MAX_ATTEMPTS"}, {"PORT", "08080", "PORT"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe deployment setting accepted: %v", err)
			}
		})
	}
}

func TestSecurityConfigDatabaseTLSAndTimeouts(t *testing.T) {
	securityConfigTestEnvironment(t)
	for _, value := range []string{
		"", "postgres://judge:database-test-secret@db.example.test/judge", "postgres://judge:database-test-secret@db.example.test/judge?sslmode=disable",
		"postgres://judge:database-test-secret@db.example.test/judge?sslmode=prefer", "postgres://judge:database-test-secret@db.example.test/judge?sslmode=require",
		"postgres://judge:database-test-secret@db.example.test/judge?sslmode=verify-ca", "host=db.example.test user=judge password=database-test-secret sslmode=verify-full",
		"https://judge:database-test-secret@db.example.test/judge?sslmode=verify-full",
	} {
		if _, err := databaseConnectionConfig(value, true); err == nil {
			t.Fatal("production accepted a database without an explicit verified-TLS URL")
		} else if strings.Contains(err.Error(), "database-test-secret") || strings.Contains(err.Error(), "db.example.test") {
			t.Fatal("database validation error disclosed credentials or host")
		}
	}
	cfg, err := databaseConnectionConfig("postgres://judge:database-test-secret@db.example.test:5432/judge?sslmode=verify-full&statement_timeout=999999&lock_timeout=999999&connect_timeout=99", true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "db.example.test" || cfg.TLSConfig.MinVersion != tls.VersionTLS12 || cfg.ConnectTimeout != 5*time.Second || cfg.RuntimeParams["statement_timeout"] != "5000" || cfg.RuntimeParams["lock_timeout"] != "5000" {
		t.Fatal("production database config lost certificate verification or bounded timeouts")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName == "" || fallback.TLSConfig.MinVersion != tls.VersionTLS12 {
			t.Fatal("production database has an unverified fallback")
		}
	}
	if _, err := databaseConnectionConfig("postgres://judge:secret@localhost/judge?sslmode=disable", false); err != nil {
		t.Fatalf("explicit development database rejected: %v", err)
	}
}

func TestSecurityConfigProductionExecutorDigestAndIsolationGates(t *testing.T) {
	for _, key := range []string{"RUNTIME_IMAGE", "C_COMPILER_IMAGE", "CPP_COMPILER_IMAGE", "GO_COMPILER_IMAGE", "JAVA_COMPILER_IMAGE"} {
		for _, value := range []string{"reviewed/image:latest", "reviewed/image@sha256:short", "--privileged"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				securityConfigTestProduction(t)
				t.Setenv(key, value)
				if _, err := loadExecutorOptions(true); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("unreviewed/malformed image accepted: %v", err)
				}
			})
		}
	}
	for _, test := range []struct{ key, value, want string }{
		{"DOCKER_HOST", "tcp://docker.example.test:2375", "DOCKER_HOST"},
		{"DOCKER_HOST", "", "DOCKER_HOST"}, {"DOCKER_CONTEXT", "remote", "DOCKER_CONTEXT"},
		{"HOST_WORKSPACE_ROOT", "/tmp", "HOST_WORKSPACE_ROOT"}, {"HOST_WORKSPACE_ROOT", "relative/workspace", "HOST_WORKSPACE_ROOT"},
		{"TMPDIR", "/tmp", "HOST_WORKSPACE_ROOT"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			securityConfigTestProduction(t)
			t.Setenv(test.key, test.value)
			if _, err := loadExecutorOptions(true); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe executor deployment accepted: %v", err)
			}
		})
	}
	for _, kind := range []string{"nonprivate workspace", "symlink workspace"} {
		t.Run(kind, func(t *testing.T) {
			securityConfigTestProduction(t)
			root := os.Getenv("HOST_WORKSPACE_ROOT")
			if kind == "nonprivate workspace" {
				if err := os.Chmod(root, 0750); err != nil {
					t.Fatal(err)
				}
			} else {
				link := filepath.Join(t.TempDir(), "workspace-link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HOST_WORKSPACE_ROOT", link)
				t.Setenv("TMPDIR", link)
			}
			if _, err := loadExecutorOptions(true); err == nil {
				t.Fatal("unsafe workspace root accepted")
			}
		})
	}
}
