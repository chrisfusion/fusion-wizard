// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"fmt"
	"sync"
	"testing"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// TestConcurrentRunsShareOneResource: many runs ensure the same chain at once, then all roll back at
// once. The chain must be created exactly once and deleted exactly once, and the ledger must end
// empty. Run under -race.
func TestConcurrentRunsShareOneResource(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	const runs = 20
	p := map[string]string{"name": "shared-chain", "jobTemplate": "tpl"}

	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := r.ensure(t, wizardv1.StepChain, fmt.Sprintf("run-%d", i), "chain", p)
			if err != nil || !res.Done {
				t.Errorf("run-%d: done=%v err=%v", i, res.Done, err)
			}
		}(i)
	}
	wg.Wait()

	if n := r.log.count("create weave/chains/shared-chain"); n != 1 {
		t.Fatalf("shared chain created %d times, want exactly 1", n)
	}
	if e := r.entry(t, wizardv1.ServiceWeave, KindChain, "shared-chain"); e == nil || len(e.Spec.Refs) != runs {
		t.Fatalf("want %d refs, entry = %+v", runs, e)
	}

	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := r.env.Rollback(ctx, fmt.Sprintf("run-%d", i)); err != nil {
				t.Errorf("rollback run-%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if n := r.log.count("delete weave/chains/shared-chain"); n != 1 {
		t.Errorf("shared chain deleted %d times, want exactly 1 (events %v)", n, r.log.snapshot())
	}
	if entries, _ := r.led.List(ctx); len(entries) != 0 {
		t.Errorf("ledger not empty after every run rolled back: %d entries", len(entries))
	}
}

// TestSharedPipelineRollback is the scenario the ledger exists for: two runs provision the same
// pipeline for the same repository, sharing the artifact, tag, job template and chain but each owning
// its own watcher and trigger. Rolling back one run must leave everything the other still needs.
func TestSharedPipelineRollback(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.index.addArtifact(42, "app.nightly", "1.0.0")
	r.forge.setBuild(succeeded(1))

	provision := func(run, watcher, trigger, entrypoint string) {
		t.Helper()
		r.mustDone(t, wizardv1.StepGitWatcher, run, "watcher", gwParams(watcher, repoURL))
		built, err := r.wait(t, Input{Run: run, Key: "build", Params: map[string]string{"watcher": watcher}})
		if err != nil || !built.Done {
			t.Fatalf("%s waitBuild: %+v, %v", run, built, err)
		}
		o := built.Outputs
		r.mustDone(t, wizardv1.StepTag, run, "tag", map[string]string{"artifactId": o["artifactId"], "artifactName": o["artifactName"], "version": o["version"]})
		r.mustDone(t, wizardv1.StepJobTemplate, run, "template", map[string]string{"name": "shared-tpl", "artifactName": o["artifactName"], "tag": "stable"})
		r.mustDone(t, wizardv1.StepChain, run, "chain", map[string]string{"name": "shared-chain", "jobTemplate": "shared-tpl"})
		r.mustDone(t, wizardv1.StepTrigger, run, "trigger", map[string]string{"name": trigger, "chain": "shared-chain", "override.ENTRYPOINT": entrypoint})
	}
	provision("run-a", "watch-a", "trigger-a", "a.py")
	provision("run-b", "watch-b", "trigger-b", "b.py")

	if err := r.env.Rollback(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	// Only what run-a exclusively owned is gone.
	for _, gone := range [][2]string{{"triggers", "trigger-a"}} {
		if r.weave.has(gone[0], gone[1]) {
			t.Errorf("%s/%s should be deleted", gone[0], gone[1])
		}
	}
	if _, ok := r.forge.watchers["watch-a"]; ok {
		t.Error("run-a's watcher should be deleted")
	}
	for _, kept := range [][2]string{{"triggers", "trigger-b"}, {"chains", "shared-chain"}, {"jobtemplates", "shared-tpl"}} {
		if !r.weave.has(kept[0], kept[1]) {
			t.Errorf("%s/%s is still used by run-b and must survive", kept[0], kept[1])
		}
	}
	if _, ok := r.forge.watchers["watch-b"]; !ok {
		t.Error("run-b's watcher must survive")
	}
	if _, ok := r.index.artifacts["app.nightly"]; !ok {
		t.Error("the artifact is shared and must survive")
	}
	if r.log.count("delete index/") != 0 {
		t.Errorf("no index deletion expected yet: %v", r.log.snapshot())
	}

	if err := r.env.Rollback(ctx, "run-b"); err != nil {
		t.Fatal(err)
	}
	if len(r.weave.objs) != 0 || len(r.forge.watchers) != 0 || len(r.index.artifacts) != 0 {
		t.Errorf("everything must be gone: weave=%d forge=%d index=%d", len(r.weave.objs), len(r.forge.watchers), len(r.index.artifacts))
	}
	if entries, _ := r.led.List(ctx); len(entries) != 0 {
		t.Errorf("ledger must be empty, has %d entries", len(entries))
	}
	// Each shared resource was deleted exactly once, by the last run.
	for _, ev := range []string{"delete weave/chains/shared-chain", "delete weave/jobtemplates/shared-tpl", "delete index/artifact/42", "delete index/tag/42/stable"} {
		if n := r.log.count(ev); n != 1 {
			t.Errorf("%q happened %d times, want 1", ev, n)
		}
	}
}
