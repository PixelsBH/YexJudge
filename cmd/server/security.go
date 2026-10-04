package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type principal struct {
	service       string
	user          string
	role          string
	insecure      bool
	exposeDetails bool
}

type principalKey struct{}

func requestPrincipal(ctx context.Context) (principal, bool) {
	identity, ok := ctx.Value(principalKey{}).(principal)
	return identity, ok
}

type requestBudget struct {
	window   time.Time
	requests int
	creates  int
}

// One bounded window per service/user. Rates are instance-local; durable job
// quotas are separately serialized in PostgreSQL. Do not scale API replicas
// without coordinating these request budgets at the authenticated gateway.
type requestLimiter struct {
	mu        sync.Mutex
	entries   map[string]requestBudget
	lastSweep time.Time
	now       func() time.Time
}

const maxRateIdentities = 10000

func (l *requestLimiter) allow(key string, requests, creates int, create bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window := now.Truncate(time.Minute)
	if l.lastSweep != window {
		for key, entry := range l.entries {
			if entry.window.Before(window) {
				delete(l.entries, key)
			}
		}
		l.lastSweep = window
	}
	entry, exists := l.entries[key]
	if !exists && len(l.entries) >= maxRateIdentities {
		return false
	}
	if entry.window != window {
		entry = requestBudget{window: window}
	}
	if entry.requests >= requests || (create && entry.creates >= creates) {
		return false
	}
	entry.requests++
	if create {
		entry.creates++
	}
	l.entries[key] = entry
	return true
}

type securitySnapshot struct {
	AuthenticationFailures int64 `json:"authenticationFailures"`
	ForbiddenRequests      int64 `json:"forbiddenRequests"`
	RateLimitedRequests    int64 `json:"rateLimitedRequests"`
	CapacityRejections     int64 `json:"capacityRejections"`
	InFlight               int64 `json:"inFlight"`
	AcceptingSubmissions   bool  `json:"acceptingSubmissions"`
	RetentionDeleted       int64 `json:"retentionDeleted"`
	RetentionFailures      int64 `json:"retentionFailures"`
}

type apiSecurity struct {
	cfg               securityConfig
	limiter           requestLimiter
	inFlight          chan struct{}
	authFailures      atomic.Int64
	forbidden         atomic.Int64
	rateLimited       atomic.Int64
	capacityRejected  atomic.Int64
	busy              atomic.Int64
	retentionDeleted  atomic.Int64
	retentionFailures atomic.Int64
}

var runtimeSecurity *apiSecurity

func newAPISecurity(cfg securityConfig) *apiSecurity {
	return &apiSecurity{cfg: cfg, limiter: requestLimiter{entries: make(map[string]requestBudget), now: time.Now}, inFlight: make(chan struct{}, cfg.maxInFlight)}
}

func (s *apiSecurity) snapshot() securitySnapshot {
	return securitySnapshot{
		AuthenticationFailures: s.authFailures.Load(), ForbiddenRequests: s.forbidden.Load(),
		RateLimitedRequests: s.rateLimited.Load(), CapacityRejections: s.capacityRejected.Load(),
		InFlight: s.busy.Load(), AcceptingSubmissions: s.cfg.acceptSubmissions && !serverDraining.Load(),
		RetentionDeleted: s.retentionDeleted.Load(), RetentionFailures: s.retentionFailures.Load(),
	}
}

func (s *apiSecurity) authenticate(r *http.Request) (principal, bool) {
	if s.cfg.insecureLocal {
		// Loopback binding is also enforced at configuration time. Validate the
		// actual peer, never attacker-supplied forwarding headers.
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return principal{}, false
		}
		return principal{service: "local", user: "local", role: "submit", insecure: true, exposeDetails: s.cfg.exposeDetails}, true
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return principal{}, false
	}
	parts := strings.Split(values[0], " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !validServiceToken(parts[1]) {
		return principal{}, false
	}
	digest := sha256.Sum256([]byte(parts[1]))
	identity := principal{}
	found := false
	for _, credential := range s.cfg.credentials {
		if subtle.ConstantTimeCompare(digest[:], credential.digest[:]) == 1 {
			identity = principal{service: credential.service, role: credential.role, exposeDetails: s.cfg.exposeDetails}
			found = true
		}
	}
	return identity, found
}

func isSubmissionPath(path string) bool {
	return path == "/submissions" || path == "/submit" || path == "/judge" || strings.HasPrefix(path, "/submissions/")
}

func isCreateRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/submissions" || r.URL.Path == "/submit" || r.URL.Path == "/judge")
}

func (s *apiSecurity) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !s.cfg.allowedHosts[strings.ToLower(r.Host)] || !validAuthority(r.Host, s.cfg.production) {
			s.forbidden.Add(1)
			writeAPIError(w, http.StatusMisdirectedRequest, "invalid_host", "unrecognized request host")
			return
		}
		if len(r.Header.Values("Origin")) != 0 {
			s.forbidden.Add(1)
			writeAPIError(w, http.StatusForbidden, "browser_access_disabled", "use the authenticated YexCode gateway")
			return
		}
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case s.inFlight <- struct{}{}:
			s.busy.Add(1)
			defer func() { <-s.inFlight; s.busy.Add(-1) }()
		default:
			s.capacityRejected.Add(1)
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, http.StatusServiceUnavailable, "request_capacity_exceeded", "request capacity is exhausted")
			return
		}
		identity, authenticated := s.authenticate(r)
		if !authenticated {
			s.authFailures.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="YexJudge"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "service authentication is required")
			return
		}
		create := isCreateRequest(r)
		if !s.limiter.allow("service:"+identity.service, s.cfg.serviceRequests, s.cfg.serviceCreates, create) {
			s.rejectRate(w)
			return
		}
		if r.URL.Path == "/ready" || r.URL.Path == "/diagnostics" {
			if identity.role != "admin" && !identity.insecure {
				s.forbidden.Add(1)
				writeAPIError(w, http.StatusForbidden, "forbidden", "administrative service authentication is required")
				return
			}
		} else if isSubmissionPath(r.URL.Path) {
			if identity.role != "submit" {
				s.forbidden.Add(1)
				writeAPIError(w, http.StatusForbidden, "forbidden", "submission service authentication is required")
				return
			}
			if !identity.insecure {
				values := r.Header.Values("X-Yex-User-ID")
				if len(values) != 1 || !validIdentity(values[0]) {
					writeAPIError(w, http.StatusBadRequest, "invalid_user_identity", "a valid gateway-derived X-Yex-User-ID is required")
					return
				}
				identity.user = values[0]
			}
			if !s.limiter.allow("user:"+identity.service+":"+identity.user, s.cfg.userRequests, s.cfg.userCreates, create) {
				s.rejectRate(w)
				return
			}
		}
		if create && (!s.cfg.acceptSubmissions || serverDraining.Load()) {
			w.Header().Set("Retry-After", "60")
			writeAPIError(w, http.StatusServiceUnavailable, "submissions_paused", "submission admission is paused")
			return
		}
		if r.ContentLength > maxRequestBodyBytes {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, identity)))
	})
}

func (s *apiSecurity) rejectRate(w http.ResponseWriter) {
	s.rateLimited.Add(1)
	w.Header().Set("Retry-After", "60")
	writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
}

func newHTTPServer(cfg config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: net.JoinHostPort(cfg.listenHost, cfg.port), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
}
