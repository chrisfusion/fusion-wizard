// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// The fakes must satisfy the interfaces the steps consume.
var (
	_ Forge = (*stepstest.Forge)(nil)
	_ Index = (*stepstest.Index)(nil)
	_ Weave = (*stepstest.Weave)(nil)
)

// ---- rig ----

type rig struct {
	env   *Env
	forge *stepstest.Forge
	index *stepstest.Index
	weave *stepstest.Weave
	led   *ledger.Ledger
	log   *stepstest.EventLog
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
	log := &stepstest.EventLog{}
	r := &rig{
		forge: stepstest.NewForge(log), index: stepstest.NewIndex(log), weave: stepstest.NewWeave(log),
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
