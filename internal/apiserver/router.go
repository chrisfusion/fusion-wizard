// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handler returns the HTTP handler with all routes, middleware and authentication wired.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(recoveryMiddleware, loggingMiddleware)

	// Probes stay outside authentication: kubelet and the BFF health check call them anonymously.
	r.Get("/healthz", healthz)
	r.Get("/readyz", healthz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.authMiddleware)

		r.Get("/definitions", s.listDefinitions)
		r.Get("/definitions/{name}", s.getDefinition)

		r.Get("/runs", s.listRuns)
		r.Post("/runs", s.createRun)
		r.Post("/runs/bulk-rollback", s.bulkRollback)
		r.Get("/runs/{name}", s.getRun)
		r.Delete("/runs/{name}", s.deleteRun)
		r.Post("/runs/{name}/retry", s.retryRun)
		r.Post("/runs/{name}/rollback", s.rollbackRun)

		r.Get("/resources", s.listResources)
	})
	return r
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
