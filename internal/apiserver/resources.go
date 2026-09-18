// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"net/http"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// resourceView is one ledger entry: an upstream resource and every run step that uses it. It is
// read-only and mainly for answering "why was this not deleted?" (a shared resource lists its owners).
type resourceView struct {
	Service     wizardv1.ServiceName         `json:"service"`
	Kind        string                       `json:"kind"`
	Name        string                       `json:"name"`
	ExternalID  string                       `json:"externalID,omitempty"`
	SpecHash    string                       `json:"specHash,omitempty"`
	Managed     bool                         `json:"managed"`
	Terminating bool                         `json:"terminating,omitempty"`
	Refs        []wizardv1.ResourceReference `json:"refs"`
	CreatedAt   metav1.Time                  `json:"createdAt"`
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	entries, err := ledger.New(s.client, s.cfg.Namespace).List(r.Context())
	if err != nil {
		internalError(w, r, err, "kind", "WizardResource")
		return
	}
	service, kind := r.URL.Query().Get("service"), r.URL.Query().Get("kind")

	items := make([]resourceView, 0, len(entries))
	for _, e := range entries {
		if (service != "" && string(e.Spec.Service) != service) || (kind != "" && e.Spec.Kind != kind) {
			continue
		}
		refs := e.Spec.Refs
		if refs == nil {
			refs = []wizardv1.ResourceReference{}
		}
		items = append(items, resourceView{
			Service: e.Spec.Service, Kind: e.Spec.Kind, Name: e.Spec.Name, ExternalID: e.Spec.ExternalID,
			SpecHash: e.Spec.SpecHash, Managed: e.Spec.Managed, Terminating: e.Spec.Terminating,
			Refs: refs, CreatedAt: e.CreationTimestamp,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
