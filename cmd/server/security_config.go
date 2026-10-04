package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"yexjudge/internal/judge"
)

const maxConfigSecretBytes = 64 << 10

type serviceCredential struct {
	Service string `json:"service"`
	Token   string `json:"token"`
	Role    string `json:"role"`
}

type credentialDigest struct {
	service string
	role    string
	digest  [sha256.Size]byte
}

type securityConfig struct {
	production        bool
	insecureLocal     bool
	credentials       []credentialDigest
	allowedHosts      map[string]bool
	admission         judge.AdmissionLimits
	serviceRequests   int
	userRequests      int
	serviceCreates    int
	userCreates       int
	maxInFlight       int
	exposeDetails     bool
	acceptSubmissions bool
}

func loadSecurityConfig(port string) (securityConfig, error) {
	environment := getEnv("APP_ENV", "development")
	if environment != "development" && environment != "production" {
		return securityConfig{}, fmt.Errorf("APP_ENV must be development or production")
	}
	cfg := securityConfig{production: environment == "production"}
	var err error
	cfg.insecureLocal, err = getEnvBoolStrict("ALLOW_INSECURE_LOCAL", false)
	if err != nil {
		return cfg, err
	}
	if cfg.production && cfg.insecureLocal {
		return cfg, fmt.Errorf("ALLOW_INSECURE_LOCAL is forbidden in production")
	}
	credentialJSON, err := readConfigSecret("AUTH_CREDENTIALS", cfg.production)
	if err != nil {
		return cfg, err
	}
	if credentialJSON != "" {
		cfg.credentials, err = parseCredentials(credentialJSON)
		if err != nil {
			return cfg, err
		}
	}
	if cfg.insecureLocal && len(cfg.credentials) != 0 {
		return cfg, fmt.Errorf("insecure local mode cannot be combined with service credentials")
	}
	if !cfg.insecureLocal && len(cfg.credentials) == 0 {
		return cfg, fmt.Errorf("AUTH_CREDENTIALS_FILE is required; unauthenticated mode requires explicit development-only ALLOW_INSECURE_LOCAL=true")
	}
	if cfg.production {
		admin, submit := false, false
		for _, credential := range cfg.credentials {
			admin = admin || credential.role == "admin"
			submit = submit || credential.role == "submit"
		}
		if !admin || !submit {
			return cfg, fmt.Errorf("production credentials require separate admin and submit services")
		}
	}
	hosts := os.Getenv("ALLOWED_HOSTS")
	if cfg.production && hosts == "" {
		return cfg, fmt.Errorf("ALLOWED_HOSTS is required in production")
	}
	if hosts == "" {
		hosts = "localhost:" + port + ",127.0.0.1:" + port + ",[::1]:" + port
	}
	cfg.allowedHosts = make(map[string]bool)
	for _, host := range strings.Split(hosts, ",") {
		if !validAuthority(host, cfg.production) {
			return cfg, fmt.Errorf("ALLOWED_HOSTS must contain exact valid authorities without wildcards or whitespace")
		}
		cfg.allowedHosts[strings.ToLower(host)] = true
	}
	settings := []struct {
		key               string
		fallback, maximum int
		destination       *int
	}{
		{"MAX_QUEUED", 100, 10000, &cfg.admission.MaxQueued},
		{"MAX_ACTIVE_PER_SERVICE", 32, 10000, &cfg.admission.MaxActivePerService},
		{"MAX_ACTIVE_PER_USER", 2, 1000, &cfg.admission.MaxActivePerUser},
		{"SERVICE_REQUESTS_PER_MINUTE", 600, 100000, &cfg.serviceRequests},
		{"USER_REQUESTS_PER_MINUTE", 120, 10000, &cfg.userRequests},
		{"SERVICE_SUBMISSIONS_PER_MINUTE", 60, 10000, &cfg.serviceCreates},
		{"USER_SUBMISSIONS_PER_MINUTE", 10, 1000, &cfg.userCreates},
		{"HTTP_MAX_IN_FLIGHT", 64, 1024, &cfg.maxInFlight},
	}
	for _, setting := range settings {
		value, err := getEnvBoundedInt(setting.key, setting.fallback, setting.maximum)
		if err != nil {
			return cfg, err
		}
		*setting.destination = value
	}
	if cfg.admission.MaxActivePerUser > cfg.admission.MaxActivePerService || cfg.admission.MaxActivePerService > cfg.admission.MaxQueued {
		return cfg, fmt.Errorf("active user limit must not exceed service limit, and service limit must not exceed queue limit")
	}
	cfg.exposeDetails, err = getEnvBoolStrict("EXPOSE_RESULT_DETAILS", cfg.insecureLocal)
	if err != nil {
		return cfg, err
	}
	if cfg.production && cfg.exposeDetails {
		return cfg, fmt.Errorf("EXPOSE_RESULT_DETAILS must be false in production")
	}
	cfg.acceptSubmissions, err = getEnvBoolStrict("ACCEPT_SUBMISSIONS", true)
	return cfg, err
}

func parseCredentials(value string) ([]credentialDigest, error) {
	var credentials []serviceCredential
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return nil, fmt.Errorf("invalid service credential document")
	}
	if decoder.Decode(new(any)) != io.EOF || len(credentials) == 0 || len(credentials) > 64 {
		return nil, fmt.Errorf("invalid service credential document")
	}
	digests := make([]credentialDigest, 0, len(credentials))
	seen := make(map[[sha256.Size]byte]bool)
	roles := make(map[string]string)
	for _, credential := range credentials {
		if !validIdentity(credential.Service) || (credential.Role != "submit" && credential.Role != "admin") || !validServiceToken(credential.Token) {
			return nil, fmt.Errorf("service credentials require a valid service, admin/submit role, and a 32–256 character token without whitespace")
		}
		digest := sha256.Sum256([]byte(credential.Token))
		if seen[digest] || (roles[credential.Service] != "" && roles[credential.Service] != credential.Role) {
			return nil, fmt.Errorf("duplicate tokens or conflicting roles in service credentials")
		}
		seen[digest] = true
		roles[credential.Service] = credential.Role
		digests = append(digests, credentialDigest{service: credential.Service, role: credential.Role, digest: digest})
	}
	return digests, nil
}

func validServiceToken(value string) bool {
	if len(value) < 32 || len(value) > 256 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 33 || char > 126 {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool { return validRequestID(value) }

func validAuthority(authority string, dnsOnly bool) bool {
	if authority == "" || len(authority) > 260 || strings.ContainsAny(authority, "/\\@?#* \t\r\n") {
		return false
	}
	host := authority
	if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return false
		}
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed < 1 || parsed > 65535 || strconv.Itoa(parsed) != port {
			return false
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return !dnsOnly
	}
	if len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range strings.ToLower(label) {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

// Errors name only the setting, never its value or the filesystem/driver error.
func readConfigSecret(key string, production bool) (string, error) {
	value, path := os.Getenv(key), os.Getenv(key+"_FILE")
	if value != "" && path != "" {
		return "", fmt.Errorf("set only one of %s or %s_FILE", key, key)
	}
	if production && value != "" {
		return "", fmt.Errorf("production %s must use a protected %s_FILE", key, key)
	}
	if path == "" {
		return value, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigSecretBytes {
		return "", fmt.Errorf("cannot read regular bounded %s_FILE", key)
	}
	if production && info.Mode().Perm()&0027 != 0 {
		return "", fmt.Errorf("%s_FILE must not be group-writable or accessible to other users", key)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s_FILE", key)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigSecretBytes+1))
	if err != nil || len(data) > maxConfigSecretBytes {
		return "", fmt.Errorf("cannot read bounded %s_FILE", key)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", fmt.Errorf("%s_FILE is empty", key)
	}
	return strings.TrimSpace(string(data)), nil
}

func getEnvBoolStrict(key string, fallback bool) (bool, error) {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return fallback, nil
	}
	if value == "true" {
		return true, nil
	}
	if value == "false" {
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false", key)
}

func getEnvBoundedInt(key string, fallback, maximum int) (int, error) {
	value, err := getEnvIntStrict(key, fallback)
	if err != nil {
		return 0, err
	}
	if value > maximum {
		return 0, fmt.Errorf("%s must not exceed %d", key, maximum)
	}
	return value, nil
}

func databaseConnectionConfig(value string, production bool) (*pgx.ConnConfig, error) {
	if value == "" {
		return nil, fmt.Errorf("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	if production {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Query().Get("sslmode") != "verify-full" {
			return nil, fmt.Errorf("production database URL requires explicit sslmode=verify-full")
		}
	}
	cfg, err := pgx.ParseConfig(value)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	verified := func(config *tls.Config) bool {
		return config != nil && !config.InsecureSkipVerify && config.ServerName != ""
	}
	if production {
		if !verified(cfg.TLSConfig) {
			return nil, fmt.Errorf("production database requires verified TLS")
		}
		for _, fallback := range cfg.Fallbacks {
			if !verified(fallback.TLSConfig) {
				return nil, fmt.Errorf("production database requires verified TLS for every host")
			}
		}
		cfg.TLSConfig.MinVersion = tls.VersionTLS12
		for _, fallback := range cfg.Fallbacks {
			fallback.TLSConfig.MinVersion = tls.VersionTLS12
		}
	}
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams["statement_timeout"] = "5000"
	cfg.RuntimeParams["lock_timeout"] = "5000"
	return cfg, nil
}

func loadExecutorOptions(production bool) (judge.DockerExecutorOptions, error) {
	opts := judge.DockerExecutorOptions{RuntimeImage: getEnv("RUNTIME_IMAGE", judge.RuntimeSandboxImage), CompileImages: make(map[string]string)}
	images := []struct{ key, language, fallback string }{
		{"C_COMPILER_IMAGE", "c", "gcc:13"}, {"CPP_COMPILER_IMAGE", "cpp", "gcc:13"},
		{"GO_COMPILER_IMAGE", "go", "golang:1.24-alpine"}, {"JAVA_COMPILER_IMAGE", "java", "eclipse-temurin:17-jdk"},
	}
	validate := func(key, image string) error {
		if err := judge.ValidateDockerImageReference(image); err != nil {
			return fmt.Errorf("invalid %s", key)
		}
		if production && !strings.Contains(image, "@sha256:") {
			return fmt.Errorf("production %s must use a reviewed sha256 digest", key)
		}
		return nil
	}
	if err := validate("RUNTIME_IMAGE", opts.RuntimeImage); err != nil {
		return opts, err
	}
	for _, image := range images {
		value := getEnv(image.key, image.fallback)
		if err := validate(image.key, value); err != nil {
			return opts, err
		}
		opts.CompileImages[image.language] = value
	}
	if production {
		if os.Getenv("DOCKER_HOST") != "unix:///var/run/docker.sock" {
			return opts, fmt.Errorf("production DOCKER_HOST must be the dedicated local Unix socket")
		}
		if os.Getenv("DOCKER_CONTEXT") != "" {
			return opts, fmt.Errorf("production DOCKER_CONTEXT overrides are forbidden")
		}
		root := os.Getenv("HOST_WORKSPACE_ROOT")
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || os.Getenv("TMPDIR") != root || root == "/" || root == "/tmp" || root == "/home" || root == "/srv" {
			return opts, fmt.Errorf("production requires a dedicated absolute HOST_WORKSPACE_ROOT matching TMPDIR")
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return opts, fmt.Errorf("production workspace root must be a private existing directory")
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return opts, fmt.Errorf("production workspace root must not contain symlinks")
		}
	}
	return opts, nil
}
