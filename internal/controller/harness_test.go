// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/steps"
	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	ns      = "fusion"
	repoURL = "https://git.example/team/nightly.git"
)

// staticEnv is an EnvFactory that returns a fixed environment (or error).
type staticEnv struct {
	env *steps.Env
	err error
}

func (s *staticEnv) Env(context.Context) (*steps.Env, error) { return s.env, s.err }

// harness wires the reconciler to a fake cluster and fake upstreams.
type harness struct {
	t     *testing.T
	r     *WizardRunReconciler
	c     client.Client
	envs  *staticEnv
	forge *stepstest.Forge
	index *stepstest.Index
	weave *stepstest.Weave
	log   *stepstest.EventLog
	led   *ledger.Ledger
	now   time.Time
}

func newHarness(t *testing.T) *harness {
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
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&wizardv1.WizardRun{}).Build()

	log := &stepstest.EventLog{}
	h := &harness{
		t: t, c: c, log: log, now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		forge: stepstest.NewForge(log), index: stepstest.NewIndex(log), weave: stepstest.NewWeave(log),
		led: ledger.New(c, ns),
	}
	h.envs = &staticEnv{env: &steps.Env{
		Cfg: cfg, Forge: h.forge, Index: h.index, Weave: h.weave, Ledger: h.led, Now: func() time.Time { return h.now },
	}}
	h.r = &WizardRunReconciler{Client: c, Envs: h.envs, Now: func() time.Time { return h.now }}
	return h
}

func js(raw string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(raw)} }

// pythonDefinition is the shared reference definition (see stepstest.PythonJob).
func pythonDefinition() wizardv1.WizardDefinitionSpec { return *stepstest.PythonJob() }

func (h *harness) createDefinition(name string, spec wizardv1.WizardDefinitionSpec) {
	h.t.Helper()
	def := &wizardv1.WizardDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	if err := h.c.Create(context.Background(), def); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) createRun(name string, params map[string]string, entrypoints ...string) {
	h.t.Helper()
	eps := "["
	for i, e := range entrypoints {
		if i > 0 {
			eps += ","
		}
		eps += `"` + e + `"`
	}
	p := map[string]apiextensionsv1.JSON{"entrypoints": js(eps + "]")}
	for k, v := range params {
		p[k] = js(`"` + v + `"`)
	}
	run := &wizardv1.WizardRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: wizardv1.WizardRunSpec{
			DefinitionRef: corev1.LocalObjectReference{Name: "python-job"},
			Parameters:    p, DesiredState: wizardv1.DesiredApplied,
		},
	}
	if err := h.c.Create(context.Background(), run); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) reconcile(name string) ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	if err != nil {
		h.t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

// get returns the run, or nil once it is gone.
func (h *harness) get(name string) *wizardv1.WizardRun {
	h.t.Helper()
	var run wizardv1.WizardRun
	err := h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &run)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return &run
}

func (h *harness) mutateRun(name string, f func(*wizardv1.WizardRun)) {
	h.t.Helper()
	run := h.get(name)
	f(run)
	if err := h.c.Update(context.Background(), run); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) mustRun(name string) *wizardv1.WizardRun {
	h.t.Helper()
	run := h.get(name)
	if run == nil {
		h.t.Fatalf("run %s does not exist", name)
	}
	return run
}

func stepOf(run *wizardv1.WizardRun, key string) *wizardv1.WizardRunStepStatus {
	for i := range run.Status.Steps {
		if run.Status.Steps[i].Key == key {
			return &run.Status.Steps[i]
		}
	}
	return nil
}

// build fixtures ----------------------------------------------------------------------------

func buildRow(id int64, status string) upstream.Build {
	u := repoURL
	return upstream.Build{ID: id, Name: "nightly", Version: "1.0.0", Status: status, BuildType: "app", RepoURL: &u}
}

func builtRow(id int64) upstream.Build {
	b := buildRow(id, "SUCCESS")
	art, ver := int64(42), "1.0.0"
	b.IndexArtifactID, b.IndexArtifactVersion = &art, &ver
	return b
}

// forgeHasBuilt makes forge report a finished build whose artifact is in the index.
func (h *harness) forgeHasBuilt() {
	h.index.AddArtifact(42, "app.nightly", "1.0.0")
	h.forge.SetBuild(builtRow(1))
}

func (h *harness) ledgerSize() int {
	h.t.Helper()
	entries, err := h.led.List(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return len(entries)
}

func keyOf(name string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }

func ledgerKey(service, kind, name string) ledger.Key {
	return ledger.Key{Service: wizardv1.ServiceName(service), Kind: kind, Name: name}
}
