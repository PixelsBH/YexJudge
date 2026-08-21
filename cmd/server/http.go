package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxRequestBodyBytes = 1 << 20

type apiErrorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validRequestID(requestID) {
			requestID = fmt.Sprintf("%x", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') &&
			!(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') &&
			!strings.ContainsRune("-_.", char) {
			return false
		}
	}
	return true
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorResponse{
		Error: apiError{
			Code:      code,
			Message:   message,
			RequestID: w.Header().Get("X-Request-ID"),
		},
	})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeJSONDecodeError(w, err)
		return false
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeJSONDecodeError(w, err)
		return false
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	var maxError *http.MaxBytesError
	if errors.As(err, &maxError) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
		return
	}
	writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request payload")
}
