// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-wizard/internal/params"
	"fusion-platform.io/fusion-wizard/internal/plan"
	"fusion-platform.io/fusion-wizard/internal/steps"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	defaultListLimit = 100
	maxListLimit     = 500
	// maxBulkTargets stops a broad selector from rolling back an unreasonable number of runs at once.
	maxBulkTargets = 200
)

type createRunRequest struct {
	Definition string                     `json:"definition"`
	Name       string                     `json:"name,omitempty"` // generated from the definition name when empty
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

// runView is what clients see of a WizardRun: the request, who made it, and the observed status
// (phase, message and per-step progress). The definition snapshot is left out; it is large and
// only the operator needs it.
type runView struct {
	Name           string                          `json:"name"`
	Definition     string                          `json:"definition"`
	DesiredState   wizardv1.RunDesiredState        `json:"desiredState"`
	Parameters     map[string]apiextensionsv1.JSON `json:"parameters,omitempty"`
	CreatedBy      string                          `json:"createdBy,omitempty"`
	CreatedByEmail string                          `json:"createdByEmail,omitempty"`
	CreatedAt      metav1.Time                     `json:"createdAt"`
	Deleting       bool                            `json:"deleting,omitempty"`
	Status         wizardv1.WizardRunStatus        `json:"status"`
}

func viewRun(run *wizardv1.WizardRun) runView {
	v := runView{
		Name: run.Name, Definition: run.Spec.DefinitionRef.Name, DesiredState: run.Spec.DesiredState,
		Parameters: run.Spec.Parameters, CreatedBy: run.Spec.CreatedBy, CreatedByEmail: run.Spec.CreatedByEmail,
		CreatedAt: run.CreationTimestamp, Deleting: !run.DeletionTimestamp.IsZero(), Status: run.Status,
	}
	if v.DesiredState == "" {
		v.DesiredState = wizardv1.DesiredApplied
	}
	if v.Status.Phase == "" {
		v.Status.Phase = wizardv1.RunPending // not reconciled yet
	}
	return v
}

// createRun validates everything the reconciler would validate later, so a client learns about all
// problems at once instead of watching an accepted run fail.
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var req createRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Definition == "" {
		writeError(w, http.StatusBadRequest, "definition is required")
		return
	}
	if req.Name != "" {
		if msgs := validation.IsDNS1123Subdomain(req.Name); len(msgs) > 0 {
			writeError(w, http.StatusBadRequest, "invalid run name", msgs...)
			return
		}
	}

	def, ok := s.definition(w, r, req.Definition)
	if !ok {
		return
	}
	if err := steps.ValidateDefinition(&def.Spec); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "the definition is invalid", errorDetails(err)...)
		return
	}
	supplied := make(map[string]apiextensionsv1.JSON, len(req.Parameters))
	for k, raw := range req.Parameters {
		supplied[k] = apiextensionsv1.JSON{Raw: raw}
	}
	values, err := params.ResolveParameters(def.Spec.Parameters, supplied)
	if err == nil {
		_, err = plan.Expand(&def.Spec, values) // e.g. duplicate forEach items
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid parameters", errorDetails(err)...)
		return
	}

	caller := CallerFromContext(r.Context())
	run := &wizardv1.WizardRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.cfg.Namespace},
		Spec: wizardv1.WizardRunSpec{
			DefinitionRef:        corev1.LocalObjectReference{Name: def.Name},
			DefinitionSnapshot:   def.Spec.DeepCopy(),
			DefinitionGeneration: def.Generation,
			Parameters:           supplied,
			CreatedBy:            caller.UserID,
			CreatedByEmail:       caller.UserEmail,
			DesiredState:         wizardv1.DesiredApplied,
		},
	}
	if req.Name != "" {
		run.Name = req.Name
	} else {
		run.GenerateName = def.Name + "-"
	}
	if err := s.client.Create(r.Context(), run); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "a run with this name already exists")
			return
		}
		internalError(w, r, err, "kind", "WizardRun", "definition", def.Name)
		return
	}
	LoggerFromCtx(r.Context()).Info("run created", "run", run.Name, "definition", def.Name)
	w.Header().Set("Location", "/api/v1/runs/"+run.Name)
	writeJSON(w, http.StatusCreated, viewRun(run))
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultListLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "limit must be a number between 1 and "+strconv.Itoa(maxListLimit))
			return
		}
		limit = n
	}

	runs, err := s.listAllRuns(r.Context())
	if err != nil {
		internalError(w, r, err, "kind", "WizardRun")
		return
	}
	definition, phase, createdBy := q.Get("definition"), q.Get("phase"), q.Get("createdBy")
	items := make([]runView, 0, len(runs))
	for i := range runs {
		v := viewRun(&runs[i])
		if (definition != "" && v.Definition != definition) ||
			(phase != "" && string(v.Status.Phase) != phase) ||
			(createdBy != "" && v.CreatedBy != createdBy) {
			continue
		}
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool { // newest first
		if !items[i].CreatedAt.Equal(&items[j].CreatedAt) {
			return items[j].CreatedAt.Before(&items[i].CreatedAt)
		}
		return items[i].Name < items[j].Name
	})
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r, pathName(r))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, viewRun(run))
}

// retryRun asks the operator to retry a Failed run, through the one-shot retry annotation.
func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r, pathName(r))
	if !ok {
		return
	}
	if run.Status.Phase != wizardv1.RunFailed {
		phase := viewRun(run).Status.Phase
		writeError(w, http.StatusConflict, "only a failed run can be retried; this run is "+string(phase))
		return
	}
	patch := client.MergeFrom(run.DeepCopy())
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[wizardv1.AnnotationRetry] = "true"
	if err := s.client.Patch(r.Context(), run, patch); err != nil {
		notFoundOrInternal(w, r, err, "run")
		return
	}
	LoggerFromCtx(r.Context()).Info("run retry requested", "run", run.Name)
	writeJSON(w, http.StatusAccepted, viewRun(run))
}

// rollbackRun asks the operator to roll the run back. The run object stays (as RolledBack) as a
// record; deleting it is a separate call.
func (s *Server) rollbackRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r, pathName(r))
	if !ok {
		return
	}
	already := run.Spec.DesiredState == wizardv1.DesiredRolledBack
	if err := s.requestRollback(r.Context(), run); err != nil {
		notFoundOrInternal(w, r, err, "run")
		return
	}
	if !already {
		LoggerFromCtx(r.Context()).Info("run rollback requested", "run", run.Name)
	}
	status := http.StatusAccepted
	if already {
		status = http.StatusOK
	}
	writeJSON(w, status, viewRun(run))
}

// deleteRun deletes the run; the operator's finalizer rolls everything back first, so the response
// is 202 and the run disappears once that is done.
func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r, pathName(r))
	if !ok {
		return
	}
	if err := s.client.Delete(r.Context(), run); err != nil && !apierrors.IsNotFound(err) {
		internalError(w, r, err, "kind", "WizardRun", "name", run.Name)
		return
	}
	LoggerFromCtx(r.Context()).Info("run deletion requested", "run", run.Name)

	var current wizardv1.WizardRun
	err := s.client.Get(r.Context(), client.ObjectKeyFromObject(run), &current)
	switch {
	case apierrors.IsNotFound(err):
		w.WriteHeader(http.StatusNoContent) // nothing was provisioned, so nothing had to be rolled back
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, viewRun(&current))
	}
}

type bulkRollbackRequest struct {
	Names      []string `json:"names,omitempty"`
	Definition string   `json:"definition,omitempty"`
}

type bulkFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type bulkRollbackResponse struct {
	Accepted []string      `json:"accepted"`
	Failed   []bulkFailure `json:"failed"`
}

// bulkRollback rolls back many runs. It needs a selector (names and/or a definition): an empty
// request is refused instead of meaning "everything". Shared resources are safe by construction,
// since the ledger keeps a resource until the last run using it is gone.
func (s *Server) bulkRollback(w http.ResponseWriter, r *http.Request) {
	var req bulkRollbackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Names) == 0 && req.Definition == "" {
		writeError(w, http.StatusBadRequest, "give names and/or a definition to select runs; refusing to roll back everything")
		return
	}
	runs, err := s.listAllRuns(r.Context())
	if err != nil {
		internalError(w, r, err, "kind", "WizardRun")
		return
	}
	byName := make(map[string]*wizardv1.WizardRun, len(runs))
	for i := range runs {
		byName[runs[i].Name] = &runs[i]
	}

	resp := bulkRollbackResponse{Accepted: []string{}, Failed: []bulkFailure{}}
	var targets []*wizardv1.WizardRun
	if len(req.Names) > 0 {
		seen := map[string]bool{}
		for _, name := range req.Names {
			if seen[name] {
				continue
			}
			seen[name] = true
			run := byName[name]
			switch {
			case run == nil:
				resp.Failed = append(resp.Failed, bulkFailure{name, "run not found"})
			case req.Definition != "" && run.Spec.DefinitionRef.Name != req.Definition:
				resp.Failed = append(resp.Failed, bulkFailure{name, "run belongs to a different definition"})
			default:
				targets = append(targets, run)
			}
		}
	} else {
		for i := range runs {
			if runs[i].Spec.DefinitionRef.Name == req.Definition {
				targets = append(targets, &runs[i])
			}
		}
	}
	if len(targets) > maxBulkTargets {
		writeError(w, http.StatusBadRequest, "the selection matches "+strconv.Itoa(len(targets))+" runs; narrow it to at most "+strconv.Itoa(maxBulkTargets))
		return
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	for _, run := range targets {
		if err := s.requestRollback(r.Context(), run); err != nil {
			LoggerFromCtx(r.Context()).Error("bulk rollback: patch failed", "run", run.Name, "error", err)
			resp.Failed = append(resp.Failed, bulkFailure{run.Name, "could not request the rollback"})
			continue
		}
		resp.Accepted = append(resp.Accepted, run.Name)
	}
	LoggerFromCtx(r.Context()).Info("bulk rollback requested", "accepted", len(resp.Accepted), "failed", len(resp.Failed))

	status := http.StatusOK
	if len(resp.Accepted) > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

// ---- helpers ----

func (s *Server) run(w http.ResponseWriter, r *http.Request, name string) (*wizardv1.WizardRun, bool) {
	var run wizardv1.WizardRun
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &run); err != nil {
		notFoundOrInternal(w, r, err, "run")
		return nil, false
	}
	return &run, true
}

func (s *Server) listAllRuns(ctx context.Context) ([]wizardv1.WizardRun, error) {
	var list wizardv1.WizardRunList
	if err := s.client.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// requestRollback sets desiredState to RolledBack. It is idempotent.
func (s *Server) requestRollback(ctx context.Context, run *wizardv1.WizardRun) error {
	if run.Spec.DesiredState == wizardv1.DesiredRolledBack {
		return nil
	}
	patch := client.MergeFrom(run.DeepCopy())
	run.Spec.DesiredState = wizardv1.DesiredRolledBack
	return s.client.Patch(ctx, run, patch)
}
