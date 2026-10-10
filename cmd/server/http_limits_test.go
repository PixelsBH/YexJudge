package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yexjudge/internal/judge"
)

func TestNewHTTPServerBoundsConnectionsAndAllowsSubmitResponse(t *testing.T) {
	const submitTimeout = 10 * time.Second
	server := newHTTPServer(":8080", http.NotFoundHandler(), submitTimeout)
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.IdleTimeout <= 0 || server.WriteTimeout <= submitTimeout {
		t.Fatalf("HTTP timeouts are not bounded or submit response has no budget: %+v", server)
	}
	if server.ReadHeaderTimeout != serverReadHeaderTimeout || server.ReadTimeout != serverReadTimeout || server.IdleTimeout != serverIdleTimeout {
		t.Fatalf("HTTP timeouts = header %s/read %s/idle %s", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout)
	}
	if server.WriteTimeout < submitTimeout+submitResponseOverhead {
		t.Fatalf("WriteTimeout = %s, must leave submit response overhead after %s submit wait", server.WriteTimeout, submitTimeout)
	}
	if server.MaxHeaderBytes != serverMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, serverMaxHeaderBytes)
	}
}

func TestNewHTTPServerWriteTimeoutScalesWithSubmitBudget(t *testing.T) {
	server := newHTTPServer(":8080", http.NotFoundHandler(), time.Minute)
	if server.WriteTimeout < time.Minute+submitResponseOverhead {
		t.Fatalf("WriteTimeout = %s, submit budget = %s", server.WriteTimeout, time.Minute)
	}
}

func TestWriteSubmissionAdmissionError(t *testing.T) {
	t.Run("queue full is retryable service unavailable", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writeSubmissionAdmissionError(recorder, judge.ErrQueueFull)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
		}
		if recorder.Header().Get("Retry-After") == "" {
			t.Fatal("queue-full response omitted Retry-After")
		}
	})

	t.Run("internal errors remain generic", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writeSubmissionAdmissionError(recorder, errors.New("database details"))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if recorder.Body.String() == "" || recorder.Body.String() == "database details" {
			t.Fatalf("response exposed an internal error: %q", recorder.Body.String())
		}
	})
}
