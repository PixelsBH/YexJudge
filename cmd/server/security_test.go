package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yexjudge/internal/judge"
)

const (
	securityTestHost         = "judge.example.test"
	securityTestSubmitToken  = "submit-service-token-0123456789abcdef"
	securityTestRotatedToken = "rotated-submit-token-0123456789abcdef"
	securityTestOtherToken   = "other-service-token-0123456789abcdefg"
	securityTestAdminToken   = "admin-service-token-0123456789abcdefg"
)

type securityTestClock struct {
	mu   sync.Mutex
	time time.Time
}

func newSecurityTestClock() *securityTestClock {
	return &securityTestClock{time: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)}
}

func (c *securityTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

func (c *securityTestClock) advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.time = c.time.Add(duration)
}

func securityTestConfig(t *testing.T) securityConfig {
	t.Helper()
	document, err := json.Marshal([]serviceCredential{
		{Service: "gateway", Token: securityTestSubmitToken, Role: "submit"},
		{Service: "gateway", Token: securityTestRotatedToken, Role: "submit"},
		{Service: "other-gateway", Token: securityTestOtherToken, Role: "submit"},
		{Service: "operations", Token: securityTestAdminToken, Role: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := parseCredentials(string(document))
	if err != nil {
		t.Fatal(err)
	}
	return securityConfig{
		production: true, credentials: credentials,
		allowedHosts:    map[string]bool{securityTestHost: true},
		admission:       judge.AdmissionLimits{MaxQueued: 100, MaxActivePerService: 32, MaxActivePerUser: 2},
		serviceRequests: 1000, userRequests: 1000, serviceCreates: 100, userCreates: 100,
		maxInFlight: 8, acceptSubmissions: true,
	}
}

// These tests exercise process-global handlers and deliberately do not run in parallel.
func preserveSecurityTestGlobals(t *testing.T) {
	t.Helper()
	oldSecurity, oldStore, oldQueue := runtimeSecurity, submissionStore, submissionQueue
	oldPool, oldMetrics := runtimePool, runtimeMetrics
	oldWorkers, oldSlots := runtimeWorkerCapacity, runtimeCompileSlots
	oldTimeout, oldDraining := submitTimeout, serverDraining.Load()
	t.Cleanup(func() {
		runtimeSecurity, submissionStore, submissionQueue = oldSecurity, oldStore, oldQueue
		runtimePool, runtimeMetrics = oldPool, oldMetrics
		runtimeWorkerCapacity, runtimeCompileSlots = oldWorkers, oldSlots
		submitTimeout = oldTimeout
		serverDraining.Store(oldDraining)
	})
	runtimeSecurity, submissionStore, submissionQueue = nil, nil, nil
	runtimePool, runtimeMetrics = nil, nil
	runtimeWorkerCapacity, runtimeCompileSlots = 0, 0
	serverDraining.Store(false)
}

func securityTestRequest(method, path, token, user string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, "https://"+securityTestHost+path, body)
	request.RemoteAddr = "192.0.2.10:12345"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if user != "" {
		request.Header.Set("X-Yex-User-ID", user)
	}
	return request
}

func serveSecurityTestRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func requireSecurityAPIError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	var response apiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode API error: %v", err)
	}
	if response.Error.Code != code || response.Error.Message == "" {
		t.Fatalf("error = %+v, want code %q and a safe message", response.Error, code)
	}
}

func TestSecurityFailClosedRoutes(t *testing.T) {
	preserveSecurityTestGlobals(t)
	security := newAPISecurity(securityTestConfig(t))
	handler := requestIDMiddleware(security.middleware(serviceMux()))
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/submissions"}, {http.MethodPost, "/judge"}, {http.MethodPost, "/submit"},
		{http.MethodGet, "/submissions"}, {http.MethodDelete, "/submissions"},
		{http.MethodGet, "/submissions/completed-result"}, {http.MethodDelete, "/submissions/completed-result"},
		{http.MethodGet, "/ready"}, {http.MethodGet, "/diagnostics"},
	} {
		for _, token := range []string{"", strings.Repeat("x", 32)} {
			name := route.method + " " + route.path + "/missing-token"
			if token != "" {
				name = route.method + " " + route.path + "/unknown-token"
			}
			t.Run(name, func(t *testing.T) {
				// An asserted user is not an authentication credential.
				request := securityTestRequest(route.method, route.path, token, "forged-user", strings.NewReader(`{}`))
				recorder := serveSecurityTestRequest(handler, request)
				requireSecurityAPIError(t, recorder, http.StatusUnauthorized, "unauthorized")
				if recorder.Header().Get("WWW-Authenticate") != `Bearer realm="YexJudge"` {
					t.Fatal("missing bearer authentication challenge")
				}
			})
		}
	}
	if security.authFailures.Load() != 18 || security.busy.Load() != 0 || len(security.limiter.entries) != 0 {
		t.Fatalf("unauthenticated requests consumed identity budgets or leaked capacity: %+v", security.snapshot())
	}
}

func TestSecurityPublicHealthIsMinimal(t *testing.T) {
	preserveSecurityTestGlobals(t)
	security := newAPISecurity(securityTestConfig(t))
	handler := security.middleware(serviceMux())
	request := securityTestRequest(http.MethodGet, "/health", "invalid", "forged-user", nil)
	recorder := serveSecurityTestRequest(handler, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, map[string]any{"status": "ok"}) {
		t.Fatalf("public health disclosed operational details: %#v", response)
	}
	for key, value := range map[string]string{
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'", "Referrer-Policy": "no-referrer",
	} {
		if got := recorder.Header().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	if security.authFailures.Load() != 0 || len(security.limiter.entries) != 0 {
		t.Fatal("public health consumed authentication or rate budgets")
	}
}

func TestSecurityRoleSeparation(t *testing.T) {
	preserveSecurityTestGlobals(t)
	store := newSecurityTestStore()
	store.rows["result"] = judge.Submission{ID: "result", OwnerService: "gateway", OwnerUser: "alice", Status: judge.SubmissionFinished}
	submissionStore = store
	runtimeSecurity = newAPISecurity(securityTestConfig(t))
	handler := runtimeSecurity.middleware(serviceMux())
	for _, test := range []struct {
		method, path, token, user string
		status                    int
	}{
		{http.MethodGet, "/ready", securityTestSubmitToken, "alice", http.StatusForbidden},
		{http.MethodGet, "/diagnostics", securityTestSubmitToken, "alice", http.StatusForbidden},
		{http.MethodPost, "/submissions", securityTestAdminToken, "alice", http.StatusForbidden},
		{http.MethodPost, "/judge", securityTestAdminToken, "alice", http.StatusForbidden},
		{http.MethodPost, "/submit", securityTestAdminToken, "alice", http.StatusForbidden},
		{http.MethodGet, "/submissions/result", securityTestAdminToken, "alice", http.StatusForbidden},
		{http.MethodDelete, "/submissions/result", securityTestAdminToken, "alice", http.StatusForbidden},
		{http.MethodGet, "/submissions/result", securityTestSubmitToken, "alice", http.StatusOK},
		{http.MethodGet, "/diagnostics", securityTestAdminToken, "", http.StatusOK},
		{http.MethodGet, "/ready", securityTestAdminToken, "", http.StatusServiceUnavailable},
	} {
		t.Run(test.method+" "+test.path+"/"+test.token[:5], func(t *testing.T) {
			recorder := serveSecurityTestRequest(handler, securityTestRequest(test.method, test.path, test.token, test.user, strings.NewReader(`{}`)))
			if test.status == http.StatusForbidden {
				requireSecurityAPIError(t, recorder, test.status, "forbidden")
			} else if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
	if len(store.admitted) != 0 || len(store.deleted) != 0 || len(store.lookups) != 1 {
		t.Fatal("role-rejected requests reached scoped persistence")
	}
}

func TestSecurityRejectsAmbiguousAuthenticationAndUserHeaders(t *testing.T) {
	preserveSecurityTestGlobals(t)
	for _, test := range []struct {
		name          string
		authorization []string
		users         []string
		status        int
		code          string
	}{
		{"duplicate authorization same", []string{"Bearer " + securityTestSubmitToken, "Bearer " + securityTestSubmitToken}, []string{"alice"}, 401, "unauthorized"},
		{"duplicate authorization different", []string{"Bearer " + securityTestSubmitToken, "Bearer " + securityTestAdminToken}, []string{"alice"}, 401, "unauthorized"},
		{"comma joined authorization", []string{"Bearer " + securityTestSubmitToken + ", Bearer " + securityTestRotatedToken}, []string{"alice"}, 401, "unauthorized"},
		{"basic", []string{"Basic " + securityTestSubmitToken}, []string{"alice"}, 401, "unauthorized"},
		{"extra space", []string{"Bearer  " + securityTestSubmitToken}, []string{"alice"}, 401, "unauthorized"},
		{"tab separator", []string{"Bearer\t" + securityTestSubmitToken}, []string{"alice"}, 401, "unauthorized"},
		{"trailing space", []string{"Bearer " + securityTestSubmitToken + " "}, []string{"alice"}, 401, "unauthorized"},
		{"missing user", []string{"Bearer " + securityTestSubmitToken}, nil, 400, "invalid_user_identity"},
		{"duplicate user same", []string{"Bearer " + securityTestSubmitToken}, []string{"alice", "alice"}, 400, "invalid_user_identity"},
		{"duplicate user different", []string{"Bearer " + securityTestSubmitToken}, []string{"alice", "bob"}, 400, "invalid_user_identity"},
		{"comma joined user", []string{"Bearer " + securityTestSubmitToken}, []string{"alice,bob"}, 400, "invalid_user_identity"},
		{"user whitespace", []string{"Bearer " + securityTestSubmitToken}, []string{" alice"}, 400, "invalid_user_identity"},
		{"user delimiter", []string{"Bearer " + securityTestSubmitToken}, []string{"alice:admin"}, 400, "invalid_user_identity"},
		{"user too long", []string{"Bearer " + securityTestSubmitToken}, []string{strings.Repeat("u", 129)}, 400, "invalid_user_identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			security := newAPISecurity(securityTestConfig(t))
			handler := security.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid headers reached handler") }))
			request := securityTestRequest(http.MethodGet, "/submissions/result", "", "", nil)
			request.Header["Authorization"] = test.authorization
			request.Header["X-Yex-User-Id"] = test.users
			requireSecurityAPIError(t, serveSecurityTestRequest(handler, request), test.status, test.code)
		})
	}
}

func TestSecurityHostOriginAndForwardingHeaders(t *testing.T) {
	preserveSecurityTestGlobals(t)
	for _, test := range []struct {
		name, host, token string
		origins           []string
		status            int
		code              string
	}{
		{"unknown host", "evil.example.test", securityTestSubmitToken, nil, 421, "invalid_host"},
		{"unlisted port", securityTestHost + ":443", securityTestSubmitToken, nil, 421, "invalid_host"},
		{"invalid authority even if listed", "judge.example.test:0443", securityTestSubmitToken, nil, 421, "invalid_host"},
		{"production IP even if listed", "127.0.0.1", securityTestSubmitToken, nil, 421, "invalid_host"},
		{"browser", securityTestHost, securityTestSubmitToken, []string{"https://yex.example.test"}, 403, "browser_access_disabled"},
		{"null origin", securityTestHost, securityTestSubmitToken, []string{"null"}, 403, "browser_access_disabled"},
		{"empty origin", securityTestHost, securityTestSubmitToken, []string{""}, 403, "browser_access_disabled"},
		{"duplicate origin", securityTestHost, securityTestSubmitToken, []string{"null", "https://yex.example.test"}, 403, "browser_access_disabled"},
		{"forwarded auth cannot authenticate", securityTestHost, "", nil, 401, "unauthorized"},
		{"case insensitive host", strings.ToUpper(securityTestHost), securityTestSubmitToken, nil, 204, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := securityTestConfig(t)
			if strings.Contains(test.name, "even if listed") {
				cfg.allowedHosts[test.host] = true
			}
			security := newAPISecurity(cfg)
			handler := security.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := requestPrincipal(r.Context())
				if !ok || identity.service != "gateway" || identity.user != "alice" {
					t.Fatalf("forwarding headers changed principal: %+v", identity)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := securityTestRequest(http.MethodGet, "/submissions/result", test.token, "alice", nil)
			request.Host = test.host
			request.Header["Origin"] = test.origins
			request.Header.Set("Forwarded", "for=127.0.0.1;host="+securityTestHost+";proto=https")
			request.Header.Set("X-Forwarded-Host", securityTestHost)
			request.Header.Set("X-Forwarded-For", "127.0.0.1")
			request.Header.Set("X-Forwarded-Authorization", "Bearer "+securityTestSubmitToken)
			request.Header.Set("X-Forwarded-User", "administrator")
			recorder := serveSecurityTestRequest(handler, request)
			if test.code != "" {
				requireSecurityAPIError(t, recorder, test.status, test.code)
			} else if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
	// Public liveness does not bypass host or browser-origin enforcement.
	handler := newAPISecurity(securityTestConfig(t)).middleware(serviceMux())
	request := securityTestRequest(http.MethodGet, "/health", "", "", nil)
	request.Host = "evil.example.test"
	requireSecurityAPIError(t, serveSecurityTestRequest(handler, request), 421, "invalid_host")
	request.Host = securityTestHost
	request.Header.Set("Origin", "null")
	requireSecurityAPIError(t, serveSecurityTestRequest(handler, request), 403, "browser_access_disabled")
}

func TestSecurityDevelopmentUsesActualLoopbackPeer(t *testing.T) {
	preserveSecurityTestGlobals(t)
	for _, peer := range []struct {
		address string
		allowed bool
	}{
		{"127.0.0.1:1234", true}, {"[::1]:1234", true}, {"[::ffff:127.0.0.1]:1234", true},
		{"192.0.2.1:1234", false}, {"[2001:db8::1]:1234", false}, {"localhost:1234", false},
		{"127.0.0.1", false}, {"", false},
	} {
		t.Run(peer.address, func(t *testing.T) {
			cfg := securityTestConfig(t)
			cfg.production, cfg.insecureLocal, cfg.credentials = false, true, nil
			cfg.exposeDetails = true
			security := newAPISecurity(cfg)
			for _, path := range []string{"/submissions/result", "/ready", "/diagnostics"} {
				handler := security.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity, ok := requestPrincipal(r.Context())
					if !ok || identity != (principal{service: "local", user: "local", role: "submit", insecure: true, exposeDetails: true}) {
						t.Fatalf("local identity = %+v", identity)
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				request := securityTestRequest(http.MethodGet, path, "", "forged-user", nil)
				request.RemoteAddr = peer.address
				request.Header.Set("X-Forwarded-For", "127.0.0.1")
				request.Header.Set("Forwarded", "for=127.0.0.1")
				recorder := serveSecurityTestRequest(handler, request)
				if !peer.allowed {
					requireSecurityAPIError(t, recorder, 401, "unauthorized")
				} else if recorder.Code != http.StatusNoContent {
					t.Fatalf("loopback status = %d: %s", recorder.Code, recorder.Body.String())
				}
			}
		})
	}
}

func TestSecurityRequestRateLimitsAndCredentialRotation(t *testing.T) {
	preserveSecurityTestGlobals(t)
	for _, scope := range []string{"service", "user"} {
		t.Run(scope, func(t *testing.T) {
			cfg := securityTestConfig(t)
			if scope == "service" {
				cfg.serviceRequests = 2
			} else {
				cfg.userRequests = 2
			}
			security := newAPISecurity(cfg)
			clock := newSecurityTestClock()
			security.limiter.now = clock.now
			handler := security.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			for index, token := range []string{securityTestSubmitToken, securityTestRotatedToken, securityTestSubmitToken} {
				user := "alice"
				if scope == "service" {
					user = fmt.Sprintf("user-%d", index)
				}
				recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", token, user, nil))
				if index < 2 {
					if recorder.Code != 204 {
						t.Fatalf("request %d = %d", index, recorder.Code)
					}
				} else {
					requireSecurityAPIError(t, recorder, 429, "rate_limited")
					if recorder.Header().Get("Retry-After") != "60" {
						t.Fatal("rate rejection missing bounded retry hint")
					}
				}
			}
			// A different service has a distinct budget even for the same user.
			if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", securityTestOtherToken, "alice", nil)); recorder.Code != 204 {
				t.Fatalf("different service shared budget: %s", recorder.Body.String())
			}
			if scope == "user" {
				if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", securityTestSubmitToken, "bob", nil)); recorder.Code != 204 {
					t.Fatalf("different user shared budget: %s", recorder.Body.String())
				}
			}
			clock.advance(time.Minute)
			if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", securityTestRotatedToken, "alice", nil)); recorder.Code != 204 {
				t.Fatalf("next minute did not reset budget: %s", recorder.Body.String())
			}
			if security.rateLimited.Load() != 1 || security.busy.Load() != 0 {
				t.Fatalf("rate accounting = %+v", security.snapshot())
			}
		})
	}
}

func TestSecuritySeparateCreationRateAcrossAllAliases(t *testing.T) {
	preserveSecurityTestGlobals(t)
	for _, scope := range []string{"service", "user"} {
		t.Run(scope, func(t *testing.T) {
			cfg := securityTestConfig(t)
			if scope == "service" {
				cfg.serviceCreates = 2
			} else {
				cfg.userCreates = 2
			}
			security := newAPISecurity(cfg)
			clock := newSecurityTestClock()
			security.limiter.now = clock.now
			handler := security.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			for _, test := range []struct {
				method, path string
				status       int
			}{
				{http.MethodGet, "/submissions/result", 204},
				{http.MethodPost, "/submissions", 204}, {http.MethodPost, "/judge", 204}, {http.MethodPost, "/submit", 429},
				{http.MethodGet, "/submissions/result", 204}, {http.MethodDelete, "/submissions/result", 204},
			} {
				recorder := serveSecurityTestRequest(handler, securityTestRequest(test.method, test.path, securityTestSubmitToken, "alice", nil))
				if test.status == 429 {
					requireSecurityAPIError(t, recorder, 429, "rate_limited")
				} else if recorder.Code != test.status {
					t.Fatalf("%s %s = %d, want %d", test.method, test.path, recorder.Code, test.status)
				}
			}
			clock.advance(time.Minute)
			if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodPost, "/submit", securityTestRotatedToken, "alice", nil)); recorder.Code != 204 {
				t.Fatalf("creation budget did not reset: %s", recorder.Body.String())
			}
		})
	}
}

func TestSecurityLimiterConcurrentBudgetsAndBoundedCardinality(t *testing.T) {
	for _, test := range []struct {
		name     string
		create   bool
		requests int
		creates  int
		want     int64
	}{
		{"request budget", false, 31, 100, 31}, {"creation budget", true, 100, 7, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newSecurityTestClock()
			limiter := requestLimiter{entries: make(map[string]requestBudget), now: clock.now}
			var accepted atomic.Int64
			var workers sync.WaitGroup
			for i := 0; i < 128; i++ {
				workers.Go(func() {
					if limiter.allow("same-identity", test.requests, test.creates, test.create) {
						accepted.Add(1)
					}
				})
			}
			workers.Wait()
			entry := limiter.entries["same-identity"]
			if accepted.Load() != test.want || int64(entry.requests) != test.want {
				t.Fatalf("concurrent admissions = %d, entry = %+v, want %d", accepted.Load(), entry, test.want)
			}
			if test.create && int64(entry.creates) != test.want {
				t.Fatalf("concurrent creation count = %d", entry.creates)
			}
		})
	}
	clock := newSecurityTestClock()
	limiter := requestLimiter{entries: make(map[string]requestBudget), now: clock.now}
	var accepted atomic.Int64
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Go(func() {
			for index := worker; index < maxRateIdentities+512; index += 16 {
				if limiter.allow(fmt.Sprintf("identity-%d", index), 10, 10, false) {
					accepted.Add(1)
				}
			}
		})
	}
	workers.Wait()
	if accepted.Load() != maxRateIdentities || len(limiter.entries) != maxRateIdentities {
		t.Fatalf("unbounded identity cardinality: accepted=%d entries=%d", accepted.Load(), len(limiter.entries))
	}
	var existing string
	for key := range limiter.entries {
		existing = key
		break
	}
	if !limiter.allow(existing, 10, 10, false) || limiter.allow("overflow-identity", 10, 10, false) {
		t.Fatal("full map must retain existing budgets and deny new identities")
	}
	clock.advance(30 * time.Second)
	if limiter.allow("overflow-identity", 10, 10, false) {
		t.Fatal("cardinality was reset within the same minute")
	}
	clock.advance(30 * time.Second)
	if !limiter.allow("fresh-identity", 10, 10, true) || len(limiter.entries) != 1 {
		t.Fatalf("stale identities not swept at minute boundary: %d entries", len(limiter.entries))
	}
}

func TestSecurityMaxInFlightAndPublicHealthBypass(t *testing.T) {
	preserveSecurityTestGlobals(t)
	cfg := securityTestConfig(t)
	cfg.maxInFlight = 1
	security := newAPISecurity(cfg)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int64
	handler := security.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			healthHandler(w, r)
			return
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(204)
	}))
	first := httptest.NewRecorder()
	go func() {
		defer close(done)
		handler.ServeHTTP(first, securityTestRequest(http.MethodGet, "/submissions/result", securityTestSubmitToken, "alice", nil))
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("in-flight handler did not exit")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter")
	}
	recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", securityTestSubmitToken, "bob", nil))
	requireSecurityAPIError(t, recorder, 503, "request_capacity_exceeded")
	if recorder.Header().Get("Retry-After") != "1" || security.snapshot().InFlight != 1 {
		t.Fatalf("capacity accounting or retry hint incorrect: %+v", security.snapshot())
	}
	if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/health", "", "", nil)); recorder.Code != 200 {
		t.Fatalf("public health blocked on private capacity: %s", recorder.Body.String())
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("released request did not finish")
	}
	if first.Code != 204 || security.busy.Load() != 0 || len(security.inFlight) != 0 {
		t.Fatal("request leaked in-flight capacity")
	}
	requireSecurityAPIError(t, serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", "", "forged-user", nil)), 401, "unauthorized")
	if recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/result", securityTestSubmitToken, "alice", nil)); recorder.Code != 204 {
		t.Fatalf("capacity did not recover: %s", recorder.Body.String())
	}
	if security.capacityRejected.Load() != 1 || security.busy.Load() != 0 {
		t.Fatalf("final accounting = %+v", security.snapshot())
	}
}

func TestSecurityHTTPServerHasBoundedResources(t *testing.T) {
	server := newHTTPServer(config{listenHost: "::1", port: "8080"}, http.NotFoundHandler())
	if server.Addr != "[::1]:8080" || server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second ||
		server.WriteTimeout != 25*time.Second || server.IdleTimeout != 30*time.Second || server.MaxHeaderBytes != 16<<10 {
		t.Fatalf("unsafe HTTP server resource limits: %+v", server)
	}
}
