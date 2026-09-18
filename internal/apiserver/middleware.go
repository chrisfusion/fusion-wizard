// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type loggerKey struct{}

// WithLogger stores l on ctx for downstream handlers.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFromCtx returns the per-request logger, which carries request_id, method, path and (after
// authentication) principal and user_id. Handlers must use it instead of the bare slog functions.
func LoggerFromCtx(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware stamps a per-request logger and writes one access-log line per request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		logger := slog.Default().With("request_id", hex.EncodeToString(b), "method", r.Method, "path", r.URL.Path, "client_ip", r.RemoteAddr)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(WithLogger(r.Context(), logger)))
		logger.Info("request", "status", sw.status, "latency_ms", time.Since(start).Milliseconds())
	})
}

// recoveryMiddleware turns a handler panic into a logged 500 instead of a dropped connection.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				LoggerFromCtx(r.Context()).Error("panic recovered", "panic", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
