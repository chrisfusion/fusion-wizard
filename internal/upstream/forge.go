// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GitWatcherSpec is the spec part of a forge GitWatcher response (camelCase, as forge serves it).
type GitWatcherSpec struct {
	RepoURL    string `json:"repoURL"`
	RepoRef    string `json:"repoRef,omitempty"`
	BuildType  string `json:"buildType"`
	ProjectDir string `json:"projectDir,omitempty"`
}

// GitWatcherStatus is the status part of a forge GitWatcher response.
type GitWatcherStatus struct {
	Phase               string `json:"phase,omitempty"`
	LastSeenCommit      string `json:"lastSeenCommit,omitempty"`
	LastBuiltVersion    string `json:"lastBuiltVersion,omitempty"`
	LastBuildName       string `json:"lastBuildName,omitempty"`
	LastBuildVersion    string `json:"lastBuildVersion,omitempty"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastError           string `json:"lastError,omitempty"`
	Message             string `json:"message,omitempty"`
}

// GitWatcher is forge's representation of a GitWatcher CR.
type GitWatcher struct {
	Name      string           `json:"name"`
	Namespace string           `json:"namespace"`
	CreatedAt time.Time        `json:"createdAt"`
	Spec      GitWatcherSpec   `json:"spec"`
	Status    GitWatcherStatus `json:"status"`
}

// CreateGitWatcherRequest is the body of POST /api/v1/gitwatchers (snake_case, as forge expects).
// Only the fields the wizard steps use are modelled.
type CreateGitWatcherRequest struct {
	Name       string `json:"name"`
	RepoURL    string `json:"repo_url"`
	RepoRef    string `json:"repo_ref,omitempty"`
	BuildType  string `json:"build_type"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// Build is a forge build row (app, git or venv build).
type Build struct {
	ID                   int64     `json:"id"`
	Name                 string    `json:"name"`
	Version              string    `json:"version"`
	Status               string    `json:"status"` // PENDING, BUILDING, SUCCESS, FAILED
	BuildType            string    `json:"buildType"`
	IndexArtifactID      *int64    `json:"indexArtifactId"`
	IndexArtifactVersion *string   `json:"indexArtifactVersion"`
	CIBuildName          *string   `json:"ciBuildName"`
	RepoURL              *string   `json:"repoUrl,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
}

// Terminal reports whether the build will not change state any more.
func (b Build) Terminal() bool { return b.Status == "SUCCESS" || b.Status == "FAILED" }

// ForgeClient talks to fusion-forge.
type ForgeClient struct{ c *Client }

func NewForge(baseURL string, tokens TokenSource) *ForgeClient {
	return &ForgeClient{c: newClient("forge", baseURL, tokens)}
}

// GetGitWatcher returns ErrNotFound (via errors.Is) when the watcher does not exist.
func (f *ForgeClient) GetGitWatcher(ctx context.Context, name string) (*GitWatcher, error) {
	var gw GitWatcher
	if err := f.c.do(ctx, http.MethodGet, "/api/v1/gitwatchers/"+url.PathEscape(name), nil, nil, &gw); err != nil {
		return nil, err
	}
	return &gw, nil
}

// CreateGitWatcher creates a watcher; forge verifies the repository is reachable first (422 if not).
func (f *ForgeClient) CreateGitWatcher(ctx context.Context, req CreateGitWatcherRequest) (*GitWatcher, error) {
	var gw GitWatcher
	if err := f.c.do(ctx, http.MethodPost, "/api/v1/gitwatchers", nil, req, &gw); err != nil {
		return nil, err
	}
	return &gw, nil
}

// DeleteGitWatcher is idempotent: a missing watcher is not an error.
func (f *ForgeClient) DeleteGitWatcher(ctx context.Context, name string) error {
	return ignoreNotFound(f.c.do(ctx, http.MethodDelete, "/api/v1/gitwatchers/"+url.PathEscape(name), nil, nil, nil))
}

// ListAppBuilds returns up to pageSize app builds whose name matches, in forge's default order.
func (f *ForgeClient) ListAppBuilds(ctx context.Context, name string, pageSize int) ([]Build, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	q.Set("pageSize", strconv.Itoa(pageSize))
	var page struct {
		Items []Build `json:"items"`
	}
	if err := f.c.do(ctx, http.MethodGet, "/api/v1/appbuilds", q, nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// GetAppBuild fetches one build. Forge syncs the CIBuild CR into the DB row on this call only,
// so waiting for a build must poll GetAppBuild, not ListAppBuilds.
func (f *ForgeClient) GetAppBuild(ctx context.Context, id int64) (*Build, error) {
	var b Build
	if err := f.c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/appbuilds/%d", id), nil, nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}
