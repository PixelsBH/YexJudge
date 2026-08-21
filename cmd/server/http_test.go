package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodyRejectsUnknownAndTrailingValues(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"language":"python","unexpected":true}`},
		{name: "trailing value", body: `{"language":"python"}{"language":"python"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(test.body))
			var destination struct {
				Language string `json:"language"`
			}

			if decodeJSONBody(recorder, request, &destination) {
				t.Fatal("decodeJSONBody() accepted an invalid JSON request")
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDecodeJSONBodyRejectsOversizedRequests(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/run",
		strings.NewReader(`{"language":"`+strings.Repeat("x", maxRequestBodyBytes)+`"}`),
	)
	var destination map[string]any

	if decodeJSONBody(recorder, request, &destination) {
		t.Fatal("decodeJSONBody() accepted an oversized request")
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRequestIDMiddlewareReturnsSafeRequestID(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := httptest.NewRequest(http.MethodGet, "/health", nil)
	valid.Header.Set("X-Request-ID", "client-request-7")
	validRecorder := httptest.NewRecorder()
	handler.ServeHTTP(validRecorder, valid)
	if got := validRecorder.Header().Get("X-Request-ID"); got != "client-request-7" {
		t.Fatalf("valid request ID = %q, want client-request-7", got)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/health", nil)
	invalid.Header.Set("X-Request-ID", "bad request id")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalid)
	if got := invalidRecorder.Header().Get("X-Request-ID"); got == "" || got == "bad request id" {
		t.Fatalf("invalid request ID was not replaced: %q", got)
	}
}

func TestRunHandlerReturnsStructuredAdmissionError(t *testing.T) {
	previousAdmission := runAdmission
	runAdmission = make(chan struct{}, 1)
	runAdmission <- struct{}{}
	defer func() { runAdmission = previousAdmission }()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{
		"language":"python",
		"sourceCode":"print(1)",
		"limits":{"timeLimitMs":1000,"memoryLimitMb":128}
	}`))
	request.Header.Set("X-Request-ID", "admission-test")

	requestIDMiddleware(http.HandlerFunc(runHandler)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "admission-test" {
		t.Fatalf("request ID = %q, want admission-test", got)
	}
	var response apiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != "run_capacity_exhausted" || response.Error.RequestID != "admission-test" {
		t.Fatalf("error response = %+v, want structured admission error", response)
	}
}
