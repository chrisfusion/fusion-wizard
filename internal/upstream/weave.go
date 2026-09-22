// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package upstream

import (
	"context"
	"net/http"
	"net/url"
)

// WeaveAPIVersion is the apiVersion of every weave custom resource.
const WeaveAPIVersion = "weave.fusion-platform.io/v1alpha1"

// Weave REST collection names (path segments under /api/v1).
const (
	WeaveJobTemplates = "jobtemplates"
	WeaveChains       = "chains"
	WeaveTriggers     = "triggers"
)

// WeaveObject is a weave custom resource as generic JSON. The wizard treats weave specs as
// opaque data it builds and hashes, so it does not import fusion-weave's Go types (which would
// couple the two modules' release cycles).
type WeaveObject = map[string]any

// NewWeaveObject builds the envelope of a weave custom resource.
func NewWeaveObject(kind, name string, spec map[string]any) WeaveObject {
	return WeaveObject{
		"apiVersion": WeaveAPIVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}
}

// WeaveClient talks to the fusion-weave REST API. The API forces the namespace to its own.
type WeaveClient struct{ c *Client }

func NewWeave(baseURL string, tokens TokenSource) *WeaveClient {
	return &WeaveClient{c: newClient("weave", baseURL, tokens)}
}

func weavePath(collection, name string) string {
	p := "/api/v1/" + collection
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p
}

// Get returns the resource, or ErrNotFound (via errors.Is) when it does not exist.
func (w *WeaveClient) Get(ctx context.Context, collection, name string) (WeaveObject, error) {
	var obj WeaveObject
	if err := w.c.do(ctx, http.MethodGet, weavePath(collection, name), nil, nil, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// Create posts the resource; an existing name yields an error matching ErrConflict.
func (w *WeaveClient) Create(ctx context.Context, collection string, obj WeaveObject) (WeaveObject, error) {
	var created WeaveObject
	if err := w.c.do(ctx, http.MethodPost, weavePath(collection, ""), nil, obj, &created); err != nil {
		return nil, err
	}
	return created, nil
}

// Delete is idempotent: a missing resource is not an error.
func (w *WeaveClient) Delete(ctx context.Context, collection, name string) error {
	return ignoreNotFound(w.c.do(ctx, http.MethodDelete, weavePath(collection, name), nil, nil, nil))
}

// Fire asks weave to create one run from the named WeaveTrigger immediately, the same way
// spectra's UI does: PATCH the trigger with the "fusion-platform.io/fire" annotation.
func (w *WeaveClient) Fire(ctx context.Context, name string) error {
	patch := WeaveObject{"metadata": map[string]any{"annotations": map[string]any{"fusion-platform.io/fire": "true"}}}
	return w.c.do(ctx, http.MethodPatch, weavePath(WeaveTriggers, name), nil, patch, nil)
}
