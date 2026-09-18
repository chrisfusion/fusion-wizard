// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const maxBodyBytes = 1 << 20

// errorBody is the error shape used across the platform's services and read by spectra
// ({"error": "..."}); details lists every problem when there are several (parameter validation).
type errorBody struct {
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string, details ...string) {
	writeJSON(w, status, errorBody{Error: msg, Details: details})
}

// internalError logs the real error with the request logger and returns a generic 500: Kubernetes
// errors name internal objects and must not reach API clients.
func internalError(w http.ResponseWriter, r *http.Request, err error, fields ...any) {
	LoggerFromCtx(r.Context()).Error("kubernetes operation failed", append([]any{"error", err}, fields...)...)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// notFoundOrInternal maps a Kubernetes error to 404 or a generic 500.
func notFoundOrInternal(w http.ResponseWriter, r *http.Request, err error, what string) {
	if apierrors.IsNotFound(err) {
		writeError(w, http.StatusNotFound, what+" not found")
		return
	}
	internalError(w, r, err)
}

// decodeJSON reads a size-limited JSON body and rejects unknown fields, so a misspelled field is an
// error instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathName(r *http.Request) string { return chi.URLParam(r, "name") }

// errorDetails flattens an errors.Join (or any tree of joined errors) into individual messages.
func errorDetails(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range j.Unwrap() {
				walk(inner)
			}
			return
		}
		out = append(out, e.Error())
	}
	if err != nil {
		walk(err)
	}
	return out
}
