// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const indexPageSize = 100

// Artifact is a fusion-index artifact.
type Artifact struct {
	ID          int64   `json:"id"`
	FullName    string  `json:"fullName"`
	Description *string `json:"description"`
}

// Tag is a mutable pointer from a name to an artifact version.
type Tag struct {
	Tag       string `json:"tag"`
	VersionID int64  `json:"versionId"`
}

// Version is one semver version of an artifact.
type Version struct {
	ID         int64  `json:"id"`
	ArtifactID int64  `json:"artifactId"`
	Version    string `json:"version"`
	Tags       []Tag  `json:"tags"`
}

// IndexClient talks to fusion-index.
type IndexClient struct{ c *Client }

func NewIndex(baseURL string, tokens TokenSource) *IndexClient {
	return &IndexClient{c: newClient("index", baseURL, tokens)}
}

// FindArtifact returns the artifact with exactly this full name, or ErrNotFound (via errors.Is).
// The index's ?name= filter is a prefix match, so app.foo also lists app.foo-bar; this pages
// through the matches and compares names exactly.
func (i *IndexClient) FindArtifact(ctx context.Context, fullName string) (*Artifact, error) {
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("name", fullName)
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(indexPageSize))
		var res struct {
			Items []Artifact `json:"items"`
			Total int64      `json:"total"`
		}
		if err := i.c.do(ctx, http.MethodGet, "/api/v1/artifacts", q, nil, &res); err != nil {
			return nil, err
		}
		for k := range res.Items {
			if res.Items[k].FullName == fullName {
				return &res.Items[k], nil
			}
		}
		if len(res.Items) < indexPageSize || int64((page+1)*indexPageSize) >= res.Total {
			return nil, &APIError{Service: "index", Method: http.MethodGet, Path: "/api/v1/artifacts",
				Status: http.StatusNotFound, Message: fmt.Sprintf("artifact %q not found", fullName)}
		}
	}
}

// ListVersions returns every version of an artifact (the endpoint returns a bare array).
func (i *IndexClient) ListVersions(ctx context.Context, artifactID int64) ([]Version, error) {
	var vs []Version
	if err := i.c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/artifacts/%d/versions", artifactID), nil, nil, &vs); err != nil {
		return nil, err
	}
	return vs, nil
}

// SetTag points tag at version (semver "x.y.z"); an existing tag is moved.
func (i *IndexClient) SetTag(ctx context.Context, artifactID int64, tag, version string) error {
	body := struct {
		Version string `json:"version"`
	}{version}
	return i.c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/artifacts/%d/tags/%s", artifactID, url.PathEscape(tag)), nil, body, nil)
}

// DeleteTag is idempotent.
func (i *IndexClient) DeleteTag(ctx context.Context, artifactID int64, tag string) error {
	return ignoreNotFound(i.c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/artifacts/%d/tags/%s", artifactID, url.PathEscape(tag)), nil, nil, nil))
}

// DeleteArtifact removes the artifact with all versions, tags and files. Idempotent.
func (i *IndexClient) DeleteArtifact(ctx context.Context, artifactID int64) error {
	return ignoreNotFound(i.c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/artifacts/%d", artifactID), nil, nil, nil))
}
