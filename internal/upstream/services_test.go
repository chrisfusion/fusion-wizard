// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func decodeBody(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestForgeGitWatcherLifecycle(t *testing.T) {
	var gotCreate map[string]any
	deleted := 0
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/gitwatchers/missing":
			writeJSON(w, 404, `{"error":"gitwatcher not found"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/gitwatchers/nightly":
			writeJSON(w, 200, `{"name":"nightly","namespace":"fusion","spec":{"repoURL":"https://g/x.git","repoRef":"main","buildType":"app","projectDir":"sub"},"status":{"phase":"Active","lastBuiltVersion":"1.0.0","consecutiveFailures":1}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/gitwatchers":
			_ = decodeBody(r, &gotCreate)
			writeJSON(w, 201, `{"name":"nightly","spec":{"repoURL":"https://g/x.git","buildType":"app"},"status":{}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/gitwatchers/nightly":
			deleted++
			w.WriteHeader(204)
		case r.Method == http.MethodDelete:
			writeJSON(w, 404, `{"error":"gitwatcher not found"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	f := NewForge(url, nil)
	ctx := context.Background()

	if _, err := f.GetGitWatcher(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing watcher: want ErrNotFound, got %v", err)
	}
	gw, err := f.GetGitWatcher(ctx, "nightly")
	if err != nil || gw.Spec.RepoURL != "https://g/x.git" || gw.Spec.ProjectDir != "sub" || gw.Status.LastBuiltVersion != "1.0.0" || gw.Status.ConsecutiveFailures != 1 {
		t.Fatalf("GetGitWatcher: %+v, %v", gw, err)
	}

	if _, err := f.CreateGitWatcher(ctx, CreateGitWatcherRequest{Name: "nightly", RepoURL: "https://g/x.git", BuildType: "app"}); err != nil {
		t.Fatal(err)
	}
	if gotCreate["repo_url"] != "https://g/x.git" || gotCreate["build_type"] != "app" || gotCreate["name"] != "nightly" {
		t.Errorf("create body must be snake_case, got %v", gotCreate)
	}
	if _, present := gotCreate["repo_ref"]; present {
		t.Error("an empty repo_ref must be omitted so forge applies its default")
	}

	if err := f.DeleteGitWatcher(ctx, "nightly"); err != nil || deleted != 1 {
		t.Errorf("delete: %v (deleted=%d)", err, deleted)
	}
	if err := f.DeleteGitWatcher(ctx, "already-gone"); err != nil {
		t.Errorf("deleting a missing watcher must be a no-op, got %v", err)
	}
}

func TestForgeBuilds(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/appbuilds":
			if r.URL.Query().Get("name") != "nightly" || r.URL.Query().Get("pageSize") != "5" {
				t.Errorf("query = %v", r.URL.Query())
			}
			writeJSON(w, 200, `{"items":[{"id":7,"name":"nightly","version":"1.0.0","status":"BUILDING"}],"total":1,"page":0,"pageSize":5}`)
		case "/api/v1/appbuilds/7":
			writeJSON(w, 200, `{"id":7,"name":"nightly","version":"1.0.0","status":"SUCCESS","indexArtifactId":42,"indexArtifactVersion":"1.0.0","ciBuildName":"forge-app-7"}`)
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	})
	f := NewForge(url, nil)

	builds, err := f.ListAppBuilds(context.Background(), "nightly", 5)
	if err != nil || len(builds) != 1 || builds[0].ID != 7 || builds[0].Terminal() {
		t.Fatalf("ListAppBuilds: %+v, %v", builds, err)
	}
	b, err := f.GetAppBuild(context.Background(), 7)
	if err != nil || !b.Terminal() || b.IndexArtifactID == nil || *b.IndexArtifactID != 42 || *b.IndexArtifactVersion != "1.0.0" {
		t.Fatalf("GetAppBuild: %+v, %v", b, err)
	}
	if (Build{Status: "FAILED"}).Terminal() != true || (Build{Status: "PENDING"}).Terminal() {
		t.Error("Terminal() must be true for SUCCESS/FAILED only")
	}
}

func TestIndexFindArtifactExactMatchAcrossPages(t *testing.T) {
	// The index filters by prefix: "app.foo" also returns "app.foo-bar". Page 0 is full of decoys,
	// the exact match sits on page 1.
	var pages []string
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pages = append(pages, q.Get("page"))
		if q.Get("name") != "app.foo" || q.Get("pageSize") != "100" {
			t.Errorf("query = %v", q)
		}
		var items []string
		if q.Get("page") == "0" {
			for i := 0; i < 100; i++ {
				items = append(items, fmt.Sprintf(`{"id":%d,"fullName":"app.foo-%d"}`, i+1, i))
			}
		} else {
			items = []string{`{"id":900,"fullName":"app.foo"}`}
		}
		writeJSON(w, 200, `{"items":[`+strings.Join(items, ",")+`],"total":101,"page":`+q.Get("page")+`,"pageSize":100}`)
	})
	a, err := NewIndex(url, nil).FindArtifact(context.Background(), "app.foo")
	if err != nil || a.ID != 900 {
		t.Fatalf("FindArtifact: %+v, %v", a, err)
	}
	if strings.Join(pages, ",") != "0,1" {
		t.Errorf("pages requested = %v", pages)
	}
}

func TestIndexFindArtifactNotFound(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"items":[{"id":1,"fullName":"app.foo-bar"}],"total":1,"page":0,"pageSize":100}`)
	})
	_, err := NewIndex(url, nil).FindArtifact(context.Background(), "app.foo")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a prefix-only match is not a match; want ErrNotFound, got %v", err)
	}
}

func TestIndexVersionsTagsAndDeletes(t *testing.T) {
	var tagBody map[string]string
	var calls []string
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/artifacts/42/versions":
			writeJSON(w, 200, `[{"id":5,"artifactId":42,"version":"1.0.0","tags":[{"tag":"stable","versionId":5}]}]`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/artifacts/42/tags/"):
			_ = decodeBody(r, &tagBody)
			w.WriteHeader(200)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/gone"):
			writeJSON(w, 404, `{"error":"tag not found"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/artifacts/404":
			writeJSON(w, 404, `{"error":"artifact not found"}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	idx := NewIndex(url, nil)
	ctx := context.Background()

	vs, err := idx.ListVersions(ctx, 42)
	if err != nil || len(vs) != 1 || vs[0].Version != "1.0.0" || vs[0].Tags[0].Tag != "stable" {
		t.Fatalf("ListVersions: %+v, %v", vs, err)
	}
	if err := idx.SetTag(ctx, 42, "stable", "1.0.0"); err != nil || tagBody["version"] != "1.0.0" {
		t.Errorf("SetTag: %v, body=%v", err, tagBody)
	}
	for name, err := range map[string]error{
		"DeleteTag":              idx.DeleteTag(ctx, 42, "stable"),
		"DeleteTag missing":      idx.DeleteTag(ctx, 42, "gone"),
		"DeleteArtifact":         idx.DeleteArtifact(ctx, 42),
		"DeleteArtifact missing": idx.DeleteArtifact(ctx, 404),
	} {
		if err != nil {
			t.Errorf("%s must succeed (idempotent), got %v", name, err)
		}
	}
	if err := idx.SetTag(ctx, 42, "a/b", "1.0.0"); err != nil {
		t.Errorf("SetTag with a slash: %v", err)
	}
	last := calls[len(calls)-1]
	if last != "PUT /api/v1/artifacts/42/tags/a%2Fb" {
		t.Errorf("tag names must be path-escaped, last call = %q", last)
	}
}

func TestWeaveResources(t *testing.T) {
	var created map[string]any
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/jobtemplates/tpl":
			writeJSON(w, 200, `{"apiVersion":"weave.fusion-platform.io/v1alpha1","kind":"WeaveJobTemplate","metadata":{"name":"tpl"},"spec":{"image":"img"}}`)
		case r.Method == http.MethodGet:
			writeJSON(w, 404, `{"code":404,"message":"not found"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/chains":
			_ = decodeBody(r, &created)
			writeJSON(w, 201, `{"kind":"WeaveChain","metadata":{"name":"c1","resourceVersion":"9"}}`)
		case r.Method == http.MethodPost:
			writeJSON(w, 409, `{"code":409,"message":"already exists"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/triggers/t1":
			writeJSON(w, 200, `{}`)
		case r.Method == http.MethodDelete:
			writeJSON(w, 404, `{"code":404,"message":"not found"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	w := NewWeave(url, nil)
	ctx := context.Background()

	obj, err := w.Get(ctx, WeaveJobTemplates, "tpl")
	if err != nil || obj["spec"].(map[string]any)["image"] != "img" {
		t.Fatalf("Get: %v, %v", obj, err)
	}
	if _, err := w.Get(ctx, WeaveJobTemplates, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}

	spec := map[string]any{"steps": []any{map[string]any{"name": "run"}}}
	out, err := w.Create(ctx, WeaveChains, NewWeaveObject("WeaveChain", "c1", spec))
	if err != nil || out["metadata"].(map[string]any)["resourceVersion"] != "9" {
		t.Fatalf("Create: %v, %v", out, err)
	}
	if created["apiVersion"] != WeaveAPIVersion || created["kind"] != "WeaveChain" || created["metadata"].(map[string]any)["name"] != "c1" {
		t.Errorf("envelope = %v", created)
	}
	if _, err := w.Create(ctx, WeaveTriggers, NewWeaveObject("WeaveTrigger", "t1", nil)); !errors.Is(err, ErrConflict) {
		t.Errorf("Create duplicate: want ErrConflict, got %v", err)
	}

	if err := w.Delete(ctx, WeaveTriggers, "t1"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := w.Delete(ctx, WeaveTriggers, "gone"); err != nil {
		t.Errorf("deleting a missing resource must be a no-op, got %v", err)
	}
}
