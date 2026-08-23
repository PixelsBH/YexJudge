package main

import (
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
			request := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(test.body))
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
		"/submit",
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
