package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yexjudge/internal/judge"
)

type securityTestScope struct{ id, service, user string }

type securityTestStore struct {
	mu          sync.Mutex
	rows        map[string]judge.Submission
	admitted    []judge.Submission
	limits      []judge.AdmissionLimits
	lookups     []securityTestScope
	deleted     []securityTestScope
	updated     []judge.Submission
	admitError  error
	getError    error
	deleteError error
	updateError error
}

var _ judge.SubmissionStore = (*securityTestStore)(nil)
var _ admittedSubmissionStore = (*securityTestStore)(nil)

func newSecurityTestStore() *securityTestStore {
	return &securityTestStore{rows: make(map[string]judge.Submission)}
}

func (*securityTestStore) Save(judge.Submission) error {
	panic("HTTP admission must not use unscoped Save")
}

func (*securityTestStore) Get(string) (judge.Submission, bool) {
	panic("HTTP lookup must not use unscoped Get")
}

func (s *securityTestStore) Update(submission judge.Submission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = append(s.updated, submission)
	if s.updateError != nil {
		return s.updateError
	}
	s.rows[submission.ID] = submission
	return nil
}

func (s *securityTestStore) SaveAdmitted(_ context.Context, submission judge.Submission, limits judge.AdmissionLimits) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitted = append(s.admitted, submission)
	s.limits = append(s.limits, limits)
	if s.admitError != nil {
		return s.admitError
	}
	if _, exists := s.rows[submission.ID]; exists {
		return errors.New("duplicate submission ID")
	}
	s.rows[submission.ID] = submission
	return nil
}

func (s *securityTestStore) GetOwned(_ context.Context, id, service, user string) (judge.Submission, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups = append(s.lookups, securityTestScope{id, service, user})
	if s.getError != nil {
		return judge.Submission{}, false, s.getError
	}
	submission, found := s.rows[id]
	if !found || service == "" || user == "" || submission.OwnerService != service || submission.OwnerUser != user {
		return judge.Submission{}, false, nil
	}
	return submission, true, nil
}

func (s *securityTestStore) DeleteOwned(_ context.Context, id, service, user string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, securityTestScope{id, service, user})
	if s.deleteError != nil {
		return false, s.deleteError
	}
	submission, found := s.rows[id]
	if !found || service == "" || user == "" || submission.OwnerService != service || submission.OwnerUser != user {
		return false, nil
	}
	if submission.Status == judge.SubmissionQueued || submission.Status == judge.SubmissionRunning {
		return false, judge.ErrSubmissionActive
	}
	delete(s.rows, id)
	return true, nil
}

func (*securityTestStore) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (*securityTestStore) Ready(context.Context) error { return nil }
func (*securityTestStore) Counts() (judge.SubmissionCounts, error) {
	return judge.SubmissionCounts{}, nil
}

func (s *securityTestStore) complete(id string, result *judge.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	submission := s.rows[id]
	submission.Status, submission.Result = judge.SubmissionFinished, result
	s.rows[id] = submission
}

type securityTestQueue struct {
	mu           sync.Mutex
	enqueued     []string
	enqueueError error
	onEnqueue    func(string)
}

var _ judge.SubmissionQueue = (*securityTestQueue)(nil)

func (q *securityTestQueue) Enqueue(id string) error {
	q.mu.Lock()
	q.enqueued = append(q.enqueued, id)
	err, callback := q.enqueueError, q.onEnqueue
	q.mu.Unlock()
	if err == nil && callback != nil {
		callback(id)
	}
	return err
}

func (*securityTestQueue) Dequeue(context.Context) (judge.SubmissionClaim, error) {
	return judge.SubmissionClaim{}, errors.New("unexpected worker dequeue in HTTP test")
}
func (*securityTestQueue) RenewLease(context.Context, judge.SubmissionClaim) error {
	return errors.New("unexpected lease renewal in HTTP test")
}
func (*securityTestQueue) RecoverExpired(context.Context) (int, error) { return 0, nil }
func (*securityTestQueue) LeaseDuration() time.Duration                { return time.Minute }
func (*securityTestQueue) Close()                                      {}

func admissionTestHarness(t *testing.T) (*securityTestStore, *securityTestQueue, http.Handler) {
	t.Helper()
	preserveSecurityTestGlobals(t)
	store, queue := newSecurityTestStore(), &securityTestQueue{}
	submissionStore, submissionQueue = store, queue
	runtimeSecurity = newAPISecurity(securityTestConfig(t))
	runtimeSecurity.limiter.now = newSecurityTestClock().now
	return store, queue, requestIDMiddleware(runtimeSecurity.middleware(serviceMux()))
}

func admissionTestJob() judge.Job {
	return judge.Job{
		Language: "go", SourceCode: "package main\nfunc main() {}",
		TestCases: []judge.TestCase{{ID: 1, Input: "protected-input", ExpectedOutput: "protected-expected"}},
		Limits:    judge.Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
}

func admissionTestBody(t *testing.T) *strings.Reader {
	t.Helper()
	body, err := json.Marshal(admissionTestJob())
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(body))
}

func TestAdmissionCreationRoutesUseAuthenticatedOwnersAndOpaqueIDs(t *testing.T) {
	for _, path := range []string{"/submissions", "/judge", "/submit"} {
		t.Run(path, func(t *testing.T) {
			store, queue, handler := admissionTestHarness(t)
			// Complete before the first synchronous poll; no worker or timer wait is needed.
			queue.onEnqueue = func(id string) { store.complete(id, &judge.Result{Status: judge.Accepted}) }
			request := securityTestRequest(http.MethodPost, path, securityTestRotatedToken, "alice", admissionTestBody(t))
			recorder := serveSecurityTestRequest(handler, request)
			wantStatus := http.StatusAccepted
			if path == "/submit" {
				wantStatus = http.StatusOK
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			if len(store.admitted) != 1 || len(queue.enqueued) != 1 {
				t.Fatalf("admitted/enqueued = %d/%d", len(store.admitted), len(queue.enqueued))
			}
			submission := store.admitted[0]
			decoded, err := hex.DecodeString(submission.ID)
			if err != nil || len(decoded) != 16 || submission.ID != strings.ToLower(submission.ID) {
				t.Fatalf("ID %q is not an opaque 128-bit hex ID", submission.ID)
			}
			if submission.OwnerService != "gateway" || submission.OwnerUser != "alice" || submission.Status != judge.SubmissionQueued || !reflect.DeepEqual(submission.Job, admissionTestJob()) {
				t.Fatalf("admitted submission = %+v", submission)
			}
			if store.limits[0] != runtimeSecurity.cfg.admission || queue.enqueued[0] != submission.ID || recorder.Header().Get("Location") != "/submissions/"+submission.ID {
				t.Fatal("admission limits, durable ID, or Location did not follow the secure admission contract")
			}
			if path == "/submit" {
				if !reflect.DeepEqual(store.lookups, []securityTestScope{{submission.ID, "gateway", "alice"}}) {
					t.Fatalf("synchronous result lookup was not scoped: %+v", store.lookups)
				}
			}
			for _, secret := range []string{"protected-input", "protected-expected", "sourceCode", "ownerService", "ownerUser"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("creation response disclosed %q", secret)
				}
			}
		})
	}
}

func TestAdmissionConcurrentOpaqueIDsRemainUnique(t *testing.T) {
	store, queue, _ := admissionTestHarness(t)
	const count = 64
	results := make(chan judge.Submission, count)
	failures := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Go(func() {
			request := securityTestRequest(http.MethodPost, "/submissions", "", "", nil)
			request = request.WithContext(context.WithValue(request.Context(), principalKey{}, principal{service: "gateway", user: "alice", role: "submit"}))
			submission, err := createAndQueueSubmission(request, admissionTestJob())
			if err != nil {
				failures <- err
				return
			}
			results <- submission
		})
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent admission: %v", err)
	}
	seen := make(map[string]bool)
	for submission := range results {
		decoded, err := hex.DecodeString(submission.ID)
		if err != nil || len(decoded) != 16 || seen[submission.ID] {
			t.Fatalf("invalid or duplicate opaque ID: %q", submission.ID)
		}
		seen[submission.ID] = true
	}
	if len(seen) != count || len(store.rows) != count || len(queue.enqueued) != count {
		t.Fatalf("concurrent IDs/rows/enqueues = %d/%d/%d", len(seen), len(store.rows), len(queue.enqueued))
	}
}

func TestAdmissionCapacityErrorsAreSafeAcrossAllCreationRoutes(t *testing.T) {
	for _, path := range []string{"/submissions", "/judge", "/submit"} {
		for _, test := range []struct {
			name, code string
			err        error
			status     int
		}{
			{"queue full", "queue_full", fmt.Errorf("wrapped: %w", judge.ErrQueueFull), 503},
			{"service active limit", "active_submission_limit", fmt.Errorf("wrapped: %w", judge.ErrServiceLimit), 429},
			{"user active limit", "active_submission_limit", fmt.Errorf("wrapped: %w", judge.ErrUserLimit), 429},
			{"database failure", "internal_error", errors.New("postgres://secret-password@db SELECT protected-input"), 500},
		} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				store, queue, handler := admissionTestHarness(t)
				store.admitError = test.err
				recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodPost, path, securityTestSubmitToken, "alice", admissionTestBody(t)))
				requireSecurityAPIError(t, recorder, test.status, test.code)
				if len(store.rows) != 0 || len(queue.enqueued) != 0 || len(store.admitted) != 1 {
					t.Fatal("rejected admission persisted or enqueued a job")
				}
				if test.status != 500 {
					if recorder.Header().Get("Retry-After") != "5" || runtimeSecurity.capacityRejected.Load() != 1 {
						t.Fatal("capacity rejection did not report retry/accounting")
					}
				} else if runtimeSecurity.capacityRejected.Load() != 0 {
					t.Fatal("persistence failure was counted as a quota rejection")
				}
				for _, secret := range []string{"secret-password", "protected-input", "SELECT", "postgres://"} {
					if strings.Contains(recorder.Body.String(), secret) {
						t.Fatalf("admission error disclosed %q", secret)
					}
				}
			})
		}
	}
}

func TestAdmissionEnqueueFailureMarksDurableSubmissionFailed(t *testing.T) {
	for _, updateFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("update-failure=%t", updateFailure), func(t *testing.T) {
			store, queue, handler := admissionTestHarness(t)
			queue.enqueueError = errors.New("queue protected-diagnostic")
			if updateFailure {
				store.updateError = errors.New("database secret-password")
			}
			recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodPost, "/submissions", securityTestSubmitToken, "alice", admissionTestBody(t)))
			requireSecurityAPIError(t, recorder, 500, "internal_error")
			if len(store.updated) != 1 || store.updated[0].Status != judge.SubmissionFailed || store.updated[0].OwnerService != "gateway" || store.updated[0].OwnerUser != "alice" {
				t.Fatalf("failed durable job update = %+v", store.updated)
			}
			if strings.Contains(recorder.Body.String(), "protected-diagnostic") || strings.Contains(recorder.Body.String(), "secret-password") {
				t.Fatal("queue or update failure leaked raw diagnostics")
			}
		})
	}
}

func TestAdmissionOwnerScopedLookupDeleteAndTokenRotation(t *testing.T) {
	for _, status := range []judge.SubmissionStatus{judge.SubmissionFinished, judge.SubmissionQueued, judge.SubmissionRunning} {
		t.Run(string(status), func(t *testing.T) {
			store, _, handler := admissionTestHarness(t)
			store.rows["owned"] = judge.Submission{ID: "owned", OwnerService: "gateway", OwnerUser: "alice", Status: status, Result: &judge.Result{Status: judge.Accepted}}
			for _, other := range []struct{ token, service, user string }{
				{securityTestSubmitToken, "gateway", "bob"},
				{securityTestOtherToken, "other-gateway", "alice"},
				{securityTestOtherToken, "other-gateway", "bob"},
			} {
				for _, method := range []string{http.MethodGet, http.MethodDelete} {
					for _, id := range []string{"owned", "missing"} {
						recorder := serveSecurityTestRequest(handler, securityTestRequest(method, "/submissions/"+id, other.token, other.user, nil))
						requireSecurityAPIError(t, recorder, 404, "not_found")
						calls := store.lookups
						if method == http.MethodDelete {
							calls = store.deleted
						}
						if calls[len(calls)-1] != (securityTestScope{id, other.service, other.user}) {
							t.Fatalf("incorrect scoped call: %+v", calls[len(calls)-1])
						}
					}
				}
			}
			if _, exists := store.rows["owned"]; !exists {
				t.Fatal("non-owner deleted a submission")
			}
			// Rotating a credential does not change service ownership.
			recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/submissions/owned", securityTestRotatedToken, "alice", nil))
			if recorder.Code != 200 || store.lookups[len(store.lookups)-1] != (securityTestScope{"owned", "gateway", "alice"}) {
				t.Fatalf("rotated credential lost ownership: %s", recorder.Body.String())
			}
			recorder = serveSecurityTestRequest(handler, securityTestRequest(http.MethodDelete, "/submissions/owned", securityTestRotatedToken, "alice", nil))
			if status == judge.SubmissionFinished {
				if recorder.Code != 204 || recorder.Body.Len() != 0 || len(store.rows) != 0 {
					t.Fatalf("owner terminal deletion failed: %s", recorder.Body.String())
				}
			} else {
				requireSecurityAPIError(t, recorder, 409, "submission_active")
				if len(store.rows) != 1 {
					t.Fatal("active owner deletion removed a job")
				}
			}
		})
	}
}

func TestAdmissionScopedPersistenceErrorsAreRedacted(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			store, _, handler := admissionTestHarness(t)
			store.getError, store.deleteError = errors.New("protected-database-diagnostic"), errors.New("protected-database-diagnostic")
			recorder := serveSecurityTestRequest(handler, securityTestRequest(method, "/submissions/result", securityTestSubmitToken, "alice", nil))
			requireSecurityAPIError(t, recorder, 503, "persistence_unavailable")
			if strings.Contains(recorder.Body.String(), "protected-database-diagnostic") {
				t.Fatal("scoped persistence error leaked")
			}
		})
	}
}

func admissionTestProtectedResult(status judge.Status) *judge.Result {
	return &judge.Result{
		Status: status, PassedTestCases: 2, TotalTestCases: 5, RuntimeMs: 12, MemoryMb: 3.5,
		ErrorMessage: "protected-raw-diagnostic /srv/private/compiler.go",
		FailedTestCase: &judge.TestCase{
			ID: 3, Input: "protected-input", ExpectedOutput: "protected-expected", ActualOutput: "protected-actual",
			Args: []json.RawMessage{json.RawMessage(`"protected-arg"`)}, Expected: json.RawMessage(`"protected-return"`),
			ConstructorArgs: []json.RawMessage{json.RawMessage(`"protected-constructor"`)},
			Operations:      []judge.OperationCall{{Name: "protected-operation", Args: []json.RawMessage{json.RawMessage(`"protected-operation-arg"`)}}},
		},
	}
}

func TestAdmissionResultRedactionIsFailClosedAndDoesNotMutateStorage(t *testing.T) {
	for _, test := range []struct {
		status  judge.Status
		message string
	}{
		{judge.Accepted, ""}, {judge.WrongAnswer, ""}, {judge.TimeLimitExceeded, ""}, {judge.MemoryLimitExceeded, ""},
		{judge.CompilationError, "compilation failed"}, {judge.RuntimeError, "execution failed"},
		{judge.InfrastructureError, "judge infrastructure is unavailable"}, {judge.ValidationError, "submission validation failed"},
		{judge.OutputLimitExceeded, "execution output exceeded the allowed limit"}, {judge.Status("future-verdict"), ""},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			stored := admissionTestProtectedResult(test.status)
			before, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			for _, ctx := range []context.Context{context.Background(), context.WithValue(context.Background(), principalKey{}, principal{service: "gateway", user: "alice"})} {
				response := responseResult(ctx, stored)
				if response == stored || response.FailedTestCase != nil || response.ErrorMessage != test.message {
					t.Fatalf("unsafe result response: %+v", response)
				}
				if response.Status != stored.Status || response.PassedTestCases != 2 || response.TotalTestCases != 5 || response.RuntimeMs != 12 || response.MemoryMb != 3.5 {
					t.Fatal("redaction changed public verdict or aggregate metrics")
				}
				response.ErrorMessage = "changed-response"
				response.PassedTestCases = 0
			}
			after, err := json.Marshal(stored)
			if err != nil || string(before) != string(after) {
				t.Fatalf("redaction mutated stored result: %s -> %s", before, after)
			}
			ctx := context.WithValue(context.Background(), principalKey{}, principal{exposeDetails: true})
			if response := responseResult(ctx, stored); response == stored || !reflect.DeepEqual(response, stored) {
				t.Fatal("explicit detail opt-in did not return an independent result value")
			}
		})
	}
	if responseResult(context.Background(), nil) != nil {
		t.Fatal("nil pending result must stay nil")
	}
}

func TestAdmissionResultEndpointsRedactProtectedDetails(t *testing.T) {
	for _, path := range []string{"/submissions/result", "/submit"} {
		t.Run(path, func(t *testing.T) {
			store, queue, handler := admissionTestHarness(t)
			stored := admissionTestProtectedResult(judge.RuntimeError)
			before, _ := json.Marshal(stored)
			store.rows["result"] = judge.Submission{ID: "result", OwnerService: "gateway", OwnerUser: "alice", Status: judge.SubmissionFinished, Result: stored, Job: admissionTestJob(), FailureMessage: "protected-persistence-failure"}
			queue.onEnqueue = func(id string) { store.complete(id, stored) }
			request := securityTestRequest(http.MethodGet, path, securityTestSubmitToken, "alice", nil)
			if path == "/submit" {
				request = securityTestRequest(http.MethodPost, path, securityTestSubmitToken, "alice", admissionTestBody(t))
			}
			recorder := serveSecurityTestRequest(handler, request)
			if recorder.Code != 200 {
				t.Fatalf("result status = %d: %s", recorder.Code, recorder.Body.String())
			}
			var response judge.SubmissionResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Result == nil || response.Result.FailedTestCase != nil || response.Result.ErrorMessage != "execution failed" {
				t.Fatalf("unredacted endpoint response: %+v", response)
			}
			for _, secret := range []string{"protected-", "failedTestCase", "sourceCode", "ownerService", "ownerUser", "/srv/private"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("result endpoint leaked %q", secret)
				}
			}
			after, _ := json.Marshal(stored)
			if string(before) != string(after) {
				t.Fatal("endpoint redaction mutated persisted result")
			}
		})
	}
}

func TestAdmissionPauseAndShutdownRejectCreatesButAllowExistingResults(t *testing.T) {
	for _, draining := range []bool{false, true} {
		t.Run(fmt.Sprintf("draining=%t", draining), func(t *testing.T) {
			store, queue, handler := admissionTestHarness(t)
			runtimeSecurity.cfg.acceptSubmissions = draining
			serverDraining.Store(draining)
			store.rows["existing"] = judge.Submission{ID: "existing", OwnerService: "gateway", OwnerUser: "alice", Status: judge.SubmissionFinished, Result: &judge.Result{Status: judge.Accepted}}
			for _, path := range []string{"/submissions", "/judge", "/submit"} {
				recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodPost, path, securityTestSubmitToken, "alice", admissionTestBody(t)))
				requireSecurityAPIError(t, recorder, 503, "submissions_paused")
				if recorder.Header().Get("Retry-After") != "60" {
					t.Fatal("paused admission lacks retry hint")
				}
			}
			if len(store.admitted) != 0 || len(queue.enqueued) != 0 || runtimeSecurity.snapshot().AcceptingSubmissions {
				t.Fatal("paused/draining server admitted work or reports accepting submissions")
			}
			for _, path := range []string{"/submissions/existing", "/health", "/diagnostics"} {
				token := securityTestSubmitToken
				if path == "/diagnostics" {
					token = securityTestAdminToken
				}
				recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, path, token, "alice", nil))
				if recorder.Code != 200 {
					t.Fatalf("existing read %s blocked during pause/drain: %s", path, recorder.Body.String())
				}
			}
			if draining {
				recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodGet, "/ready", securityTestAdminToken, "", nil))
				var response ReadinessResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != 503 || response.Checks["server"] != "draining" {
					t.Fatalf("draining readiness = %s (error %v)", recorder.Body.String(), err)
				}
			}
			recorder := serveSecurityTestRequest(handler, securityTestRequest(http.MethodDelete, "/submissions/existing", securityTestSubmitToken, "alice", nil))
			if recorder.Code != 204 {
				t.Fatalf("terminal deletion blocked during pause/drain: %s", recorder.Body.String())
			}
		})
	}
}

func TestAdmissionWithoutAuthenticatedOwnerOrScopedStoreFailsClosed(t *testing.T) {
	store, queue, _ := admissionTestHarness(t)
	for _, identity := range []principal{{}, {service: "gateway"}, {user: "alice"}} {
		request := securityTestRequest(http.MethodPost, "/submissions", "", "", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalKey{}, identity))
		if _, err := createAndQueueSubmission(request, admissionTestJob()); err == nil {
			t.Fatalf("admitted incomplete identity: %+v", identity)
		}
	}
	request := securityTestRequest(http.MethodPost, "/submissions", "", "", nil)
	if _, err := createAndQueueSubmission(request, admissionTestJob()); err == nil {
		t.Fatal("admitted request without principal")
	}
	if _, _, err := ownedSubmission(request.Context(), "result"); err == nil {
		t.Fatal("owner lookup without principal did not fail closed")
	}
	if len(store.admitted) != 0 || len(queue.enqueued) != 0 || len(store.lookups) != 0 {
		t.Fatal("unauthenticated helper call reached persistence")
	}
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, principal{service: "gateway", user: "alice", role: "submit"}))
	runtimeSecurity = nil
	if _, err := createAndQueueSubmission(request, admissionTestJob()); err == nil {
		t.Fatal("admitted request with security runtime unavailable")
	}
	// Existing legacy stores satisfy SubmissionStore but cannot bypass scoped admission.
	submissionStore = diagnosticsStore{}
	if _, err := createAndQueueSubmission(request, admissionTestJob()); err == nil {
		t.Fatal("legacy store bypassed scoped admission")
	}
	if _, _, err := ownedSubmission(request.Context(), "result"); err == nil {
		t.Fatal("legacy store bypassed scoped lookup")
	}
}
