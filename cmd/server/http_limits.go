package main

import (
	"errors"
	"net/http"
	"time"

	"yexjudge/internal/judge"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 10 * time.Second
	serverIdleTimeout       = 60 * time.Second
	serverMaxHeaderBytes    = 16 << 10
	submitResponseOverhead  = 15 * time.Second
)

func newHTTPServer(address string, handler http.Handler, submitTimeout time.Duration) *http.Server {
	writeTimeout := submitTimeout + submitResponseOverhead
	if writeTimeout < 20*time.Second {
		writeTimeout = 20 * time.Second
	}
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

func writeSubmissionAdmissionError(w http.ResponseWriter, err error) {
	if errors.Is(err, judge.ErrQueueFull) {
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, http.StatusServiceUnavailable, "queue_full", "submission queue is full; retry later")
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal error")
}
