// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package stepstest provides in-memory fakes of forge, index and weave for tests of the step
// catalogue and of the run reconciler. Every mutation is appended to a shared EventLog so tests can
// assert what happened and in which order.
package stepstest

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"fusion-platform.io/fusion-wizard/internal/upstream"
)

// APIErr builds an upstream error with the given HTTP status.
func APIErr(status int) error {
	return &upstream.APIError{Service: "fake", Method: "GET", Path: "/x", Status: status, Message: http.StatusText(status)}
}

// EventLog is a shared, ordered record of every upstream mutation across the three fakes.
type EventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *EventLog) Add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *EventLog) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *EventLog) Count(prefix string) int {
	n := 0
	for _, e := range l.Snapshot() {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (l *EventLog) IndexOf(event string) int {
	for i, e := range l.Snapshot() {
		if e == event {
			return i
		}
	}
	return -1
}

// ---- forge ----

type Forge struct {
	mu       sync.Mutex
	log      *EventLog
	Watchers map[string]*upstream.GitWatcher
	Builds   map[int64]*upstream.Build
}

func NewForge(log *EventLog) *Forge {
	return &Forge{log: log, Watchers: map[string]*upstream.GitWatcher{}, Builds: map[int64]*upstream.Build{}}
}

func (f *Forge) GetGitWatcher(_ context.Context, name string) (*upstream.GitWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.Watchers[name]; ok {
		cp := *w
		return &cp, nil
	}
	return nil, APIErr(404)
}

func (f *Forge) CreateGitWatcher(_ context.Context, req upstream.CreateGitWatcherRequest) (*upstream.GitWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Watchers[req.Name]; ok {
		return nil, APIErr(409)
	}
	w := &upstream.GitWatcher{Name: req.Name, Spec: upstream.GitWatcherSpec{
		RepoURL: req.RepoURL, RepoRef: req.RepoRef, BuildType: req.BuildType, ProjectDir: req.ProjectDir,
	}, Status: upstream.GitWatcherStatus{Phase: "Active"}}
	f.Watchers[req.Name] = w
	f.log.Add("create forge/gitwatcher/%s", req.Name)
	return w, nil
}

func (f *Forge) DeleteGitWatcher(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Watchers, name)
	f.log.Add("delete forge/gitwatcher/%s", name)
	return nil
}

func (f *Forge) ListAppBuilds(_ context.Context, _ string, _ int) ([]upstream.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]upstream.Build, 0, len(f.Builds))
	for _, b := range f.Builds {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID }) // newest first, like forge
	return out, nil
}

func (f *Forge) GetAppBuild(_ context.Context, id int64) (*upstream.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.Builds[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, APIErr(404)
}

func (f *Forge) SetBuild(b upstream.Build) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := b
	f.Builds[b.ID] = &cp
}

func (f *Forge) SetWatcherStatus(name string, st upstream.GitWatcherStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Watchers[name].Status = st
}

// ---- index ----

type Index struct {
	mu        sync.Mutex
	log       *EventLog
	Artifacts map[string]*upstream.Artifact
	Versions  map[int64][]upstream.Version
}

func NewIndex(log *EventLog) *Index {
	return &Index{log: log, Artifacts: map[string]*upstream.Artifact{}, Versions: map[int64][]upstream.Version{}}
}

func (x *Index) AddArtifact(id int64, fullName string, versions ...string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.Artifacts[fullName] = &upstream.Artifact{ID: id, FullName: fullName}
	for i, v := range versions {
		x.Versions[id] = append(x.Versions[id], upstream.Version{ID: id*100 + int64(i), ArtifactID: id, Version: v})
	}
}

func (x *Index) FindArtifact(_ context.Context, fullName string) (*upstream.Artifact, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if a, ok := x.Artifacts[fullName]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, APIErr(404)
}

func (x *Index) ListVersions(_ context.Context, id int64) ([]upstream.Version, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs, ok := x.Versions[id]
	if !ok {
		return nil, APIErr(404)
	}
	return append([]upstream.Version(nil), vs...), nil
}

func (x *Index) SetTag(_ context.Context, id int64, tag, version string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs := x.Versions[id]
	found := false
	for i := range vs {
		var kept []upstream.Tag
		for _, t := range vs[i].Tags {
			if t.Tag != tag {
				kept = append(kept, t)
			}
		}
		vs[i].Tags = kept
		if vs[i].Version == version {
			vs[i].Tags = append(vs[i].Tags, upstream.Tag{Tag: tag, VersionID: vs[i].ID})
			found = true
		}
	}
	if !found {
		return APIErr(404)
	}
	x.log.Add("settag index/%d/%s=%s", id, tag, version)
	return nil
}

func (x *Index) DeleteTag(_ context.Context, id int64, tag string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs := x.Versions[id]
	for i := range vs {
		var kept []upstream.Tag
		for _, t := range vs[i].Tags {
			if t.Tag != tag {
				kept = append(kept, t)
			}
		}
		vs[i].Tags = kept
	}
	x.log.Add("delete index/tag/%d/%s", id, tag)
	return nil
}

func (x *Index) DeleteArtifact(_ context.Context, id int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for name, a := range x.Artifacts {
		if a.ID == id {
			delete(x.Artifacts, name)
		}
	}
	delete(x.Versions, id)
	x.log.Add("delete index/artifact/%d", id)
	return nil
}

// ---- weave ----

type Weave struct {
	mu   sync.Mutex
	log  *EventLog
	Objs map[string]upstream.WeaveObject
	// DeleteErr, if set, makes every Delete fail with it (rollback failure tests).
	DeleteErr error
	// CreateHook, if set, runs instead of the normal create and decides its result.
	CreateHook func(w *Weave, collection string, obj upstream.WeaveObject) error
	// FireErr, if set, makes every Fire fail with it.
	FireErr error
	// CreateBatchTriggerErr / PatchLabelsErr, if set, make every such call fail with it.
	CreateBatchTriggerErr error
	PatchLabelsErr        error
}

func NewWeave(log *EventLog) *Weave {
	return &Weave{log: log, Objs: map[string]upstream.WeaveObject{}}
}

func (w *Weave) Get(_ context.Context, collection, name string) (upstream.WeaveObject, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o, ok := w.Objs[collection+"/"+name]; ok {
		return o, nil
	}
	return nil, APIErr(404)
}

func (w *Weave) Create(_ context.Context, collection string, obj upstream.WeaveObject) (upstream.WeaveObject, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.CreateHook != nil {
		if err := w.CreateHook(w, collection, obj); err != nil {
			return nil, err
		}
	}
	key := collection + "/" + obj["metadata"].(map[string]any)["name"].(string)
	if _, ok := w.Objs[key]; ok {
		return nil, APIErr(409)
	}
	w.Objs[key] = obj
	w.log.Add("create weave/%s", key)
	return obj, nil
}

func (w *Weave) Delete(_ context.Context, collection, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.DeleteErr != nil {
		return w.DeleteErr
	}
	delete(w.Objs, collection+"/"+name)
	w.log.Add("delete weave/%s/%s", collection, name)
	return nil
}

func (w *Weave) Fire(_ context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FireErr != nil {
		return w.FireErr
	}
	w.log.Add("fire weave/triggers/%s", name)
	return nil
}

// CreateBatchTrigger fakes weave's dedicated /batchtriggers endpoint: the resulting object is
// stored under the same "triggers/<name>" key a generic Get(triggers, name) looks up, matching
// real weave (a BatchCron trigger is still a WeaveTrigger, just created through a different path).
func (w *Weave) CreateBatchTrigger(_ context.Context, name, chain, jobs string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.CreateBatchTriggerErr != nil {
		return w.CreateBatchTriggerErr
	}
	key := "triggers/" + name
	if _, ok := w.Objs[key]; ok {
		return APIErr(409)
	}
	w.Objs[key] = upstream.NewWeaveObject("WeaveTrigger", name, map[string]any{
		"type": "BatchCron", "chainRef": map[string]any{"name": chain}, "jobs": jobs,
	})
	w.log.Add("create weave/%s", key)
	return nil
}

func (w *Weave) DeleteBatchTrigger(_ context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.DeleteErr != nil {
		return w.DeleteErr
	}
	delete(w.Objs, "triggers/"+name)
	w.log.Add("delete weave/triggers/%s", name)
	return nil
}

func (w *Weave) PatchLabels(_ context.Context, name string, labels map[string]string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.PatchLabelsErr != nil {
		return w.PatchLabelsErr
	}
	obj, ok := w.Objs["triggers/"+name]
	if !ok {
		return APIErr(404)
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	meta["labels"] = labels
	w.log.Add("label weave/triggers/%s", name)
	return nil
}

// Seed stores an object as if it had been created by hand.
func (w *Weave) Seed(collection, name string, spec map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Objs[collection+"/"+name] = upstream.NewWeaveObject("X", name, spec)
}

func (w *Weave) Has(collection, name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.Objs[collection+"/"+name]
	return ok
}
