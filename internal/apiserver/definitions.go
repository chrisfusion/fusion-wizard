// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"net/http"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-wizard/internal/steps"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// definitionView is what clients see of a WizardDefinition. Spec carries the parameter schema a
// frontend renders its form from. Valid is false for a definition that would be rejected when a run
// is created (a typo in a hand-written definition), with the reason in Error.
type definitionView struct {
	Name       string                        `json:"name"`
	Generation int64                         `json:"generation"`
	Valid      bool                          `json:"valid"`
	Error      string                        `json:"error,omitempty"`
	Spec       wizardv1.WizardDefinitionSpec `json:"spec"`
}

func viewDefinition(d *wizardv1.WizardDefinition) definitionView {
	v := definitionView{Name: d.Name, Generation: d.Generation, Valid: true, Spec: d.Spec}
	if err := steps.ValidateDefinition(&d.Spec); err != nil {
		v.Valid, v.Error = false, err.Error()
	}
	return v
}

func (s *Server) listDefinitions(w http.ResponseWriter, r *http.Request) {
	var list wizardv1.WizardDefinitionList
	if err := s.client.List(r.Context(), &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		internalError(w, r, err, "kind", "WizardDefinition")
		return
	}
	items := make([]definitionView, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, viewDefinition(&list.Items[i]))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getDefinition(w http.ResponseWriter, r *http.Request) {
	d, ok := s.definition(w, r, pathName(r))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, viewDefinition(d))
}

// definition loads a definition, writing 404 or 500 itself when it cannot.
func (s *Server) definition(w http.ResponseWriter, r *http.Request, name string) (*wizardv1.WizardDefinition, bool) {
	var d wizardv1.WizardDefinition
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &d); err != nil {
		notFoundOrInternal(w, r, err, "definition")
		return nil, false
	}
	return &d, true
}
