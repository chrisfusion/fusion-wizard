// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func apiErr(status int) error {
	return &upstream.APIError{Service: "fake", Method: "GET", Path: "/x", Status: status, Message: http.StatusText(status)}
}

// eventLog is a shared, ordered record of every upstream mutation across the three fakes.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) count(prefix string) int {
	n := 0
	for _, e := range l.snapshot() {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (l *eventLog) indexOf(event string) int {
	for i, e := range l.snapshot() {
		if e == event {
			return i
		}
	}
	return -1
}

// ---- forge ----

type fakeForge struct {
	mu       sync.Mutex
	log      *eventLog
	watchers map[string]*upstream.GitWatcher
	builds   map[int64]*upstream.Build
}

func newFakeForge(log *eventLog) *fakeForge {
	return &fakeForge{log: log, watchers: map[string]*upstream.GitWatcher{}, builds: map[int64]*upstream.Build{}}
}

func (f *fakeForge) GetGitWatcher(_ context.Context, name string) (*upstream.GitWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.watchers[name]; ok {
		cp := *w
		return &cp, nil
	}
	return nil, apiErr(404)
}

func (f *fakeForge) CreateGitWatcher(_ context.Context, req upstream.CreateGitWatcherRequest) (*upstream.GitWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.watchers[req.Name]; ok {
		return nil, apiErr(409)
	}
	w := &upstream.GitWatcher{Name: req.Name, Spec: upstream.GitWatcherSpec{
		RepoURL: req.RepoURL, RepoRef: req.RepoRef, BuildType: req.BuildType, ProjectDir: req.ProjectDir,
	}, Status: upstream.GitWatcherStatus{Phase: "Active"}}
	f.watchers[req.Name] = w
	f.log.add("create forge/gitwatcher/%s", req.Name)
	return w, nil
}

func (f *fakeForge) DeleteGitWatcher(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watchers, name)
	f.log.add("delete forge/gitwatcher/%s", name)
	return nil
}

func (f *fakeForge) ListAppBuilds(_ context.Context, _ string, _ int) ([]upstream.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]upstream.Build, 0, len(f.builds))
	for _, b := range f.builds {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID }) // newest first, like forge
	return out, nil
}

func (f *fakeForge) GetAppBuild(_ context.Context, id int64) (*upstream.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.builds[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, apiErr(404)
}

func (f *fakeForge) setBuild(b upstream.Build) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := b
	f.builds[b.ID] = &cp
}

func (f *fakeForge) setWatcherStatus(name string, st upstream.GitWatcherStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchers[name].Status = st
}

// ---- index ----

type fakeIndex struct {
	mu        sync.Mutex
	log       *eventLog
	artifacts map[string]*upstream.Artifact
	versions  map[int64][]upstream.Version
}

func newFakeIndex(log *eventLog) *fakeIndex {
	return &fakeIndex{log: log, artifacts: map[string]*upstream.Artifact{}, versions: map[int64][]upstream.Version{}}
}

func (x *fakeIndex) addArtifact(id int64, fullName string, versions ...string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.artifacts[fullName] = &upstream.Artifact{ID: id, FullName: fullName}
	for i, v := range versions {
		x.versions[id] = append(x.versions[id], upstream.Version{ID: id*100 + int64(i), ArtifactID: id, Version: v})
	}
}

func (x *fakeIndex) FindArtifact(_ context.Context, fullName string) (*upstream.Artifact, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if a, ok := x.artifacts[fullName]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, apiErr(404)
}

func (x *fakeIndex) ListVersions(_ context.Context, id int64) ([]upstream.Version, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs, ok := x.versions[id]
	if !ok {
		return nil, apiErr(404)
	}
	return append([]upstream.Version(nil), vs...), nil
}

func (x *fakeIndex) SetTag(_ context.Context, id int64, tag, version string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs := x.versions[id]
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
		return apiErr(404)
	}
	x.log.add("settag index/%d/%s=%s", id, tag, version)
	return nil
}

func (x *fakeIndex) DeleteTag(_ context.Context, id int64, tag string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	vs := x.versions[id]
	for i := range vs {
		var kept []upstream.Tag
		for _, t := range vs[i].Tags {
			if t.Tag != tag {
				kept = append(kept, t)
			}
		}
		vs[i].Tags = kept
	}
	x.log.add("delete index/tag/%d/%s", id, tag)
	return nil
}

func (x *fakeIndex) DeleteArtifact(_ context.Context, id int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for name, a := range x.artifacts {
		if a.ID == id {
			delete(x.artifacts, name)
		}
	}
	delete(x.versions, id)
	x.log.add("delete index/artifact/%d", id)
	return nil
}

// ---- weave ----

type fakeWeave struct {
	mu   sync.Mutex
	log  *eventLog
	objs map[string]upstream.WeaveObject
	// createHook, if set, runs instead of the normal create and decides its result.
	createHook func(w *fakeWeave, collection string, obj upstream.WeaveObject) error
}

func newFakeWeave(log *eventLog) *fakeWeave {
	return &fakeWeave{log: log, objs: map[string]upstream.WeaveObject{}}
}

func (w *fakeWeave) Get(_ context.Context, collection, name string) (upstream.WeaveObject, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o, ok := w.objs[collection+"/"+name]; ok {
		return o, nil
	}
	return nil, apiErr(404)
}

func (w *fakeWeave) Create(_ context.Context, collection string, obj upstream.WeaveObject) (upstream.WeaveObject, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.createHook != nil {
		if err := w.createHook(w, collection, obj); err != nil {
			return nil, err
		}
	}
	key := collection + "/" + obj["metadata"].(map[string]any)["name"].(string)
	if _, ok := w.objs[key]; ok {
		return nil, apiErr(409)
	}
	w.objs[key] = obj
	w.log.add("create weave/%s", key)
	return obj, nil
}

func (w *fakeWeave) Delete(_ context.Context, collection, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.objs, collection+"/"+name)
	w.log.add("delete weave/%s/%s", collection, name)
	return nil
}

// seed stores an object as if it had been created by hand.
func (w *fakeWeave) seed(collection, name string, spec map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.objs[collection+"/"+name] = upstream.NewWeaveObject("X", name, spec)
}

func (w *fakeWeave) has(collection, name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.objs[collection+"/"+name]
	return ok
}

// ---- rig ----

type rig struct {
	env   *Env
	forge *fakeForge
	index *fakeIndex
	weave *fakeWeave
	led   *ledger.Ledger
	log   *eventLog
	now   time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	cfg, err := instancecfg.Parse(map[string]string{
		instancecfg.KeyRunnerImage: "registry.example/runner:1.0.0",
		instancecfg.KeyForgeURL:    "http://forge:8080",
		instancecfg.KeyIndexURL:    "http://index:8080",
		instancecfg.KeyWeaveURL:    "http://weave:8082",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := wizardv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	log := &eventLog{}
	r := &rig{
		forge: newFakeForge(log), index: newFakeIndex(log), weave: newFakeWeave(log),
		led: ledger.New(fake.NewClientBuilder().WithScheme(s).Build(), "fusion"),
		log: log, now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
	r.env = &Env{Cfg: cfg, Forge: r.forge, Index: r.index, Weave: r.weave, Ledger: r.led, Now: func() time.Time { return r.now }}
	return r
}

// ensure calls a catalogue step's Ensure.
func (r *rig) ensure(t *testing.T, typ wizardv1.StepType, run, key string, params map[string]string) (Result, error) {
	t.Helper()
	step, ok := Lookup(typ)
	if !ok {
		t.Fatalf("no step %s", typ)
	}
	return step.Ensure(context.Background(), r.env, Input{Run: run, Key: key, Params: params})
}

// mustDone runs a step that must complete in one call and checks it provides every declared output.
func (r *rig) mustDone(t *testing.T, typ wizardv1.StepType, run, key string, params map[string]string) Result {
	t.Helper()
	res, err := r.ensure(t, typ, run, key, params)
	if err != nil || !res.Done {
		t.Fatalf("%s/%s %s: done=%v err=%v msg=%q", run, key, typ, res.Done, err, res.Message)
	}
	step, _ := Lookup(typ)
	for _, out := range step.Outputs() {
		if _, ok := res.Outputs[out]; !ok {
			t.Errorf("%s declares output %q but did not return it (got %v)", typ, out, res.Outputs)
		}
	}
	return res
}

func (r *rig) entry(t *testing.T, svc wizardv1.ServiceName, kind, name string) *wizardv1.WizardResource {
	t.Helper()
	e, err := r.led.Get(context.Background(), ledger.Key{Service: svc, Kind: kind, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
