// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/steps"
	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

var nightlyParams = map[string]string{"jobName": "nightly", "repoUrl": repoURL}

func TestHappyPathProvisionsEverything(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py", "etl/load.py")

	res := h.reconcile("run-a")
	if res.RequeueAfter != 0 {
		t.Errorf("a finished run must not requeue, got %v", res)
	}
	run := h.mustRun("run-a")

	if run.Status.Phase != wizardv1.RunReady || run.Status.CompletedAt == nil || run.Status.StartedAt == nil {
		t.Fatalf("status = %+v", run.Status)
	}
	if !controllerutil.ContainsFinalizer(run, wizardv1.FinalizerRollback) {
		t.Error("the rollback finalizer must be set")
	}
	if run.Spec.DefinitionSnapshot == nil || len(run.Spec.DefinitionSnapshot.Steps) != 6 {
		t.Error("the definition must be snapshotted into the run")
	}
	if c := meta.FindStatusCondition(run.Status.Conditions, wizardv1.ConditionReady); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "Ready" {
		t.Errorf("Ready condition = %+v", c)
	}

	wantKeys := []string{"watcher", "build", "tag", "template", "chain", "trigger/main.py", "trigger/etl/load.py"}
	if len(run.Status.Steps) != len(wantKeys) {
		t.Fatalf("steps = %+v", run.Status.Steps)
	}
	for i, key := range wantKeys {
		s := run.Status.Steps[i]
		if s.Key != key || s.Phase != wizardv1.StepSucceeded || len(s.Resources) != 1 {
			t.Errorf("step %d = %+v, want %s succeeded with one resource", i, s, key)
		}
	}
	if s := stepOf(run, "trigger/etl/load.py"); s.Item != "etl/load.py" || s.Name != "trigger" {
		t.Errorf("forEach instance = %+v", s)
	}

	// The definition's names resolved through params, config, step outputs and filters.
	for _, name := range []string{"nightly-main", "nightly-etl-load"} {
		if !h.weave.Has("triggers", name) {
			t.Errorf("trigger %s missing (weave: %v)", name, h.log.Snapshot())
		}
	}
	trig := h.weave.Objs["triggers/nightly-etl-load"]["spec"].(map[string]any)["parameterOverrides"].([]any)[0].(map[string]any)
	if trig["name"] != "ENTRYPOINT" || trig["value"] != "etl/load.py" {
		t.Errorf("override = %v", trig)
	}
	tpl := h.weave.Objs["jobtemplates/nightly"]["spec"].(map[string]any)
	if tpl["image"] != "registry.example/runner:1.0.0" || tpl["codeSource"].(map[string]any)["artifactName"] != "app.nightly" {
		t.Errorf("template spec = %v", tpl)
	}
	// watcher, artifact, tag, template, chain, 2 triggers
	if got := h.ledgerSize(); got != 7 {
		t.Errorf("ledger entries = %d, want 7", got)
	}
}

// TestMixedOnDemandAndCronEntrypoints is the point of Phase 4b: two entrypoints of the same run get
// independently-typed triggers from one objectList forEach, which a plain stringList forEach could
// never express (the trigger step's type/schedule params would be identical across every expansion).
func TestMixedOnDemandAndCronEntrypoints(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRunWithEntries("run-a", nightlyParams,
		entrypointEntry{file: "main.py", typ: "OnDemand"},
		entrypointEntry{file: "report.py", typ: "Cron", schedule: "0 9 * * *"},
	)

	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunReady {
		t.Fatalf("status = %+v", run.Status)
	}

	onDemand := h.weave.Objs["triggers/nightly-main"]["spec"].(map[string]any)
	if onDemand["type"] != "OnDemand" {
		t.Errorf("main.py spec = %v", onDemand)
	}
	if _, has := onDemand["schedule"]; has {
		t.Error("an OnDemand trigger must not carry a schedule")
	}

	cron := h.weave.Objs["triggers/nightly-report"]["spec"].(map[string]any)
	if cron["type"] != "Cron" || cron["schedule"] != "0 9 * * *" {
		t.Errorf("report.py spec = %v", cron)
	}
}

func TestPollingAndProgressAcrossReconciles(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.index.AddArtifact(42, "app.nightly", "1.0.0")
	h.createRun("run-a", nightlyParams, "main.py")

	res := h.reconcile("run-a") // no build exists yet
	run := h.mustRun("run-a")
	if res.RequeueAfter != 5*time.Second || run.Status.Phase != wizardv1.RunRunning {
		t.Fatalf("waiting for a build: %+v phase=%s", res, run.Status.Phase)
	}
	if s := stepOf(run, "watcher"); s.Phase != wizardv1.StepSucceeded {
		t.Errorf("the watcher step must already be done: %+v", s)
	}
	if s := stepOf(run, "build"); s.Phase != wizardv1.StepRunning || !strings.Contains(s.Message, "start a build") {
		t.Errorf("build step = %+v", s)
	}
	for _, key := range []string{"tag", "template", "chain", "trigger/main.py"} {
		if s := stepOf(run, key); s.Phase != wizardv1.StepPending {
			t.Errorf("%s must wait for the build, got %s", key, s.Phase)
		}
	}
	if !strings.Contains(run.Status.Message, "start a build") {
		t.Errorf("run message = %q", run.Status.Message)
	}

	h.forge.SetBuild(buildRow(1, "BUILDING"))
	h.reconcile("run-a")
	run = h.mustRun("run-a")
	if s := stepOf(run, "build"); s.Outputs["buildId"] != "1" || !strings.Contains(s.Message, "BUILDING") {
		t.Fatalf("the remembered build ID must be persisted between polls: %+v", s)
	}

	h.forge.SetBuild(builtRow(1))
	h.reconcile("run-a")
	if run = h.mustRun("run-a"); run.Status.Phase != wizardv1.RunReady {
		t.Fatalf("phase = %s (%s)", run.Status.Phase, run.Status.Message)
	}
	if h.log.Count("create forge/gitwatcher/") != 1 {
		t.Error("completed steps must not run again on later reconciles")
	}
}

func TestReconcilingAFinishedRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	before := h.mustRun("run-a").ResourceVersion
	events := len(h.log.Snapshot())
	for i := 0; i < 3; i++ {
		h.reconcile("run-a")
	}
	if after := h.mustRun("run-a").ResourceVersion; after != before {
		t.Errorf("a settled run was written to again (resourceVersion %s -> %s)", before, after)
	}
	if len(h.log.Snapshot()) != events {
		t.Error("a settled run must not touch upstream")
	}
}

func TestFailureAndRetry(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.index.AddArtifact(42, "app.nightly", "1.0.0")
	h.forge.SetBuild(buildRow(1, "FAILED"))
	h.createRun("run-a", nightlyParams, "main.py")

	res := h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunFailed || res.RequeueAfter != 0 {
		t.Fatalf("phase=%s res=%+v", run.Status.Phase, res)
	}
	if !strings.Contains(run.Status.Message, `step "build" failed`) || !strings.Contains(run.Status.Message, "build 1 of watcher") {
		t.Errorf("message = %q", run.Status.Message)
	}
	if stepOf(run, "build").Phase != wizardv1.StepFailed || stepOf(run, "tag").Phase != wizardv1.StepPending {
		t.Error("the failed step must be Failed and later steps must stay Pending")
	}
	if c := meta.FindStatusCondition(run.Status.Conditions, wizardv1.ConditionReady); c.Status != metav1.ConditionFalse || c.Reason != "Failed" {
		t.Errorf("condition = %+v", c)
	}

	// Without a retry request, a failed run stays failed.
	h.forge.SetBuild(builtRow(1))
	h.reconcile("run-a")
	if h.mustRun("run-a").Status.Phase != wizardv1.RunFailed {
		t.Fatal("a failed run must not recover on its own")
	}

	h.mutateRun("run-a", func(r *wizardv1.WizardRun) {
		r.Annotations = map[string]string{wizardv1.AnnotationRetry: "true"}
	})
	h.reconcile("run-a")
	run = h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunReady {
		t.Fatalf("after retry: phase=%s message=%s", run.Status.Phase, run.Status.Message)
	}
	if _, still := run.Annotations[wizardv1.AnnotationRetry]; still {
		t.Error("the retry annotation is one-shot and must be removed")
	}
	if run.Status.CompletedAt == nil || stepOf(run, "build").Failures != 0 {
		t.Errorf("status after retry = %+v", run.Status)
	}
	if h.log.Count("create forge/gitwatcher/") != 1 {
		t.Error("a retry must only redo the failed step, not the completed ones")
	}
}

func TestTransientFailureBacksOffThenGivesUp(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.weave.CreateHook = func(*stepstest.Weave, string, upstream.WeaveObject) error { return stepstest.APIErr(503) }
	h.createRun("run-a", nightlyParams, "main.py")

	wantDelay := []time.Duration{2, 4, 8, 16, 32, 60, 60, 60, 60}
	for i, d := range wantDelay {
		res := h.reconcile("run-a")
		run := h.mustRun("run-a")
		if run.Status.Phase != wizardv1.RunRunning || res.RequeueAfter != d*time.Second {
			t.Fatalf("attempt %d: phase=%s requeue=%v, want Running and %v", i+1, run.Status.Phase, res.RequeueAfter, d*time.Second)
		}
		if s := stepOf(run, "template"); s.Failures != int32(i+1) || !strings.Contains(s.Message, "will retry") {
			t.Fatalf("attempt %d: step = %+v", i+1, s)
		}
	}
	h.reconcile("run-a") // the 10th failure
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunFailed || !strings.Contains(run.Status.Message, "giving up after 10 attempts") {
		t.Fatalf("phase=%s message=%q", run.Status.Phase, run.Status.Message)
	}
}

func TestTransientFailureRecovers(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	outage := true
	h.weave.CreateHook = func(*stepstest.Weave, string, upstream.WeaveObject) error {
		if outage {
			return stepstest.APIErr(503)
		}
		return nil
	}
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")
	if run := h.mustRun("run-a"); run.Status.Phase != wizardv1.RunRunning {
		t.Fatalf("phase = %s", run.Status.Phase)
	}
	outage = false
	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunReady || stepOf(run, "template").Failures != 0 {
		t.Fatalf("phase=%s step=%+v", run.Status.Phase, stepOf(run, "template"))
	}
}

func TestPermanentUpstreamErrorFailsImmediately(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.weave.CreateHook = func(*stepstest.Weave, string, upstream.WeaveObject) error { return stepstest.APIErr(422) }
	h.createRun("run-a", nightlyParams, "main.py")

	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunFailed || stepOf(run, "template").Failures != 0 {
		t.Errorf("a 422 must fail at once: phase=%s step=%+v", run.Status.Phase, stepOf(run, "template"))
	}
}

func TestBuildTimeout(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forge.SetBuild(buildRow(1, "BUILDING"))
	h.createRun("run-a", nightlyParams, "main.py")

	h.reconcile("run-a")
	h.now = h.now.Add(11 * time.Minute) // the instance's buildTimeout is 10 minutes
	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunFailed || !strings.Contains(run.Status.Message, "timed out") {
		t.Fatalf("phase=%s message=%q", run.Status.Phase, run.Status.Message)
	}
}

func TestInvalidInputsFailTheRunWithAClearMessage(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(h *harness)
		wantSub string
	}{
		{"definition missing", func(h *harness) {}, `definition "python-job" not found`},
		{"definition invalid", func(h *harness) {
			d := pythonDefinition()
			d.Steps[0].Params["repoUrll"] = "x"
			h.createDefinition("python-job", d)
		}, "is invalid"},
		{"missing required parameter", func(h *harness) {
			h.createDefinition("python-job", pythonDefinition())
		}, `"repoUrl" is required`},
		{"duplicate forEach items", func(h *harness) {
			h.createDefinition("python-job", pythonDefinition())
		}, "is used twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.setup(h)
			switch c.name {
			case "missing required parameter":
				h.createRun("run-a", map[string]string{"jobName": "nightly"}, "main.py")
			case "duplicate forEach items":
				h.createRun("run-a", nightlyParams, "main.py", "main.py")
			default:
				h.createRun("run-a", nightlyParams, "main.py")
			}
			h.reconcile("run-a")
			run := h.mustRun("run-a")
			if run.Status.Phase != wizardv1.RunFailed || !strings.Contains(run.Status.Message, c.wantSub) {
				t.Errorf("phase=%s message=%q, want it to contain %q", run.Status.Phase, run.Status.Message, c.wantSub)
			}
			if h.log.Count("create ") != 0 {
				t.Error("nothing may be provisioned for an invalid run")
			}
		})
	}
}

func TestSnapshotProtectsARunFromDefinitionEdits(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.index.AddArtifact(42, "app.nightly", "1.0.0")
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a") // waits for the build; the snapshot is taken

	// Someone edits the live definition while the run is in flight: the trigger step disappears.
	var def wizardv1.WizardDefinition
	if err := h.c.Get(context.Background(), keyOf("python-job"), &def); err != nil {
		t.Fatal(err)
	}
	def.Spec.Steps = def.Spec.Steps[:5]
	if err := h.c.Update(context.Background(), &def); err != nil {
		t.Fatal(err)
	}

	h.forge.SetBuild(builtRow(1))
	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunReady || !h.weave.Has("triggers", "nightly-main") {
		t.Fatalf("the run must finish with the definition it started with: phase=%s", run.Status.Phase)
	}
}

func TestMissingInstanceConfigWaitsInsteadOfFailing(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.createRun("run-a", nightlyParams, "main.py")
	good := h.envs.env
	h.envs.env, h.envs.err = nil, errors.New("instance config ConfigMap fusion/wizard-config: not found")

	res := h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunRunning || res.RequeueAfter != envRetryInterval || !strings.Contains(run.Status.Message, "instance config") {
		t.Fatalf("phase=%s res=%+v message=%q", run.Status.Phase, res, run.Status.Message)
	}
	if h.log.Count("create ") != 0 {
		t.Error("nothing may be provisioned without a valid instance config")
	}

	h.envs.env, h.envs.err = good, nil
	h.forgeHasBuilt()
	h.reconcile("run-a")
	if h.mustRun("run-a").Status.Phase != wizardv1.RunReady {
		t.Error("the run must proceed once the config is valid")
	}
}

// ---- rollback and deletion ----

func TestRollbackViaDesiredState(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	h.mutateRun("run-a", func(r *wizardv1.WizardRun) { r.Spec.DesiredState = wizardv1.DesiredRolledBack })
	h.reconcile("run-a")
	run := h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunRolledBack {
		t.Fatalf("phase=%s message=%q", run.Status.Phase, run.Status.Message)
	}
	for _, s := range run.Status.Steps {
		if s.Phase != wizardv1.StepRolledBack {
			t.Errorf("step %s = %s", s.Key, s.Phase)
		}
	}
	if len(h.weave.Objs) != 0 || len(h.forge.Watchers) != 0 || len(h.index.Artifacts) != 0 || h.ledgerSize() != 0 {
		t.Errorf("everything must be gone: weave=%d forge=%d index=%d ledger=%d", len(h.weave.Objs), len(h.forge.Watchers), len(h.index.Artifacts), h.ledgerSize())
	}
	if !controllerutil.ContainsFinalizer(run, wizardv1.FinalizerRollback) {
		t.Error("the run object stays (with its finalizer) until it is deleted")
	}

	// Rolled back is terminal: repeating it is a no-op, and flipping back to Applied does not re-provision.
	events := len(h.log.Snapshot())
	h.reconcile("run-a")
	h.mutateRun("run-a", func(r *wizardv1.WizardRun) { r.Spec.DesiredState = wizardv1.DesiredApplied })
	h.reconcile("run-a")
	run = h.mustRun("run-a")
	if run.Status.Phase != wizardv1.RunRolledBack || !strings.Contains(run.Status.Message, "create a new run") || len(h.log.Snapshot()) != events {
		t.Errorf("phase=%s message=%q", run.Status.Phase, run.Status.Message)
	}
}

func TestDeletingARunRollsItBackFirst(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	if err := h.c.Delete(context.Background(), h.mustRun("run-a")); err != nil {
		t.Fatal(err)
	}
	if h.get("run-a") == nil {
		t.Fatal("the finalizer must keep the run until the rollback is done")
	}
	h.reconcile("run-a")
	if h.get("run-a") != nil {
		t.Error("the run must be gone after a successful rollback")
	}
	if len(h.weave.Objs) != 0 || len(h.forge.Watchers) != 0 || len(h.index.Artifacts) != 0 || h.ledgerSize() != 0 {
		t.Error("deleting a run must delete everything it owned")
	}
}

func TestDeletingAHalfProvisionedRunCleansUp(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.weave.CreateHook = func(*stepstest.Weave, string, upstream.WeaveObject) error { return stepstest.APIErr(503) }
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a") // watcher, artifact, tag exist; the template create fails and leaves a ledger entry

	if h.ledgerSize() == 0 {
		t.Fatal("expected ledger entries for the completed steps and the interrupted one")
	}
	if err := h.c.Delete(context.Background(), h.mustRun("run-a")); err != nil {
		t.Fatal(err)
	}
	h.weave.CreateHook = nil
	h.reconcile("run-a")
	if h.get("run-a") != nil || h.ledgerSize() != 0 || len(h.forge.Watchers) != 0 || len(h.index.Artifacts) != 0 {
		t.Errorf("cleanup incomplete: run=%v ledger=%d forge=%d index=%d", h.get("run-a") != nil, h.ledgerSize(), len(h.forge.Watchers), len(h.index.Artifacts))
	}
}

func TestFailedRollbackIsRetriedUntilItSucceeds(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	h.weave.DeleteErr = stepstest.APIErr(503)
	if err := h.c.Delete(context.Background(), h.mustRun("run-a")); err != nil {
		t.Fatal(err)
	}
	res := h.reconcile("run-a")
	run := h.get("run-a")
	if run == nil || run.Status.Phase != wizardv1.RunRollbackFailed || res.RequeueAfter != rollbackRetryInterval {
		t.Fatalf("run=%v res=%+v", run, res)
	}
	if !strings.Contains(run.Status.Message, "will retry") || !controllerutil.ContainsFinalizer(run, wizardv1.FinalizerRollback) {
		t.Errorf("message=%q; the finalizer must stay while rollback fails", run.Status.Message)
	}

	h.weave.DeleteErr = nil
	h.reconcile("run-a")
	if h.get("run-a") != nil || h.ledgerSize() != 0 || len(h.weave.Objs) != 0 {
		t.Error("the retried rollback must finish the job")
	}
}

func TestRollbackWithoutValidConfigIsRetried(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	h.envs.err = errors.New("config missing")
	h.mutateRun("run-a", func(r *wizardv1.WizardRun) { r.Spec.DesiredState = wizardv1.DesiredRolledBack })
	res := h.reconcile("run-a")
	if h.mustRun("run-a").Status.Phase != wizardv1.RunRollbackFailed || res.RequeueAfter != rollbackRetryInterval {
		t.Fatalf("res=%+v phase=%s", res, h.mustRun("run-a").Status.Phase)
	}
}

func TestTwoRunsShareResourcesAndRollBackIndependently(t *testing.T) {
	h := newHarness(t)
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	// Same job name and repository: watcher, artifact, tag, template and chain are shared; only the
	// triggers differ.
	h.createRun("run-a", nightlyParams, "a.py")
	h.createRun("run-b", nightlyParams, "b.py")
	h.reconcile("run-a")
	h.reconcile("run-b")
	for _, n := range []string{"run-a", "run-b"} {
		if run := h.mustRun(n); run.Status.Phase != wizardv1.RunReady {
			t.Fatalf("%s: phase=%s message=%q", n, run.Status.Phase, run.Status.Message)
		}
	}
	if h.log.Count("create weave/chains/") != 1 || h.log.Count("create forge/gitwatcher/") != 1 {
		t.Errorf("shared resources must be created once: %v", h.log.Snapshot())
	}
	if s := stepOf(h.mustRun("run-b"), "chain"); s.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("run-b must adopt the shared chain, got %s", s.Resources[0].Disposition)
	}

	if err := h.c.Delete(context.Background(), h.mustRun("run-a")); err != nil {
		t.Fatal(err)
	}
	h.reconcile("run-a")
	if h.get("run-a") != nil {
		t.Fatal("run-a should be gone")
	}
	if h.weave.Has("triggers", "nightly-a") {
		t.Error("run-a's own trigger must be deleted")
	}
	for _, kept := range [][2]string{{"triggers", "nightly-b"}, {"chains", "nightly"}, {"jobtemplates", "nightly"}} {
		if !h.weave.Has(kept[0], kept[1]) {
			t.Errorf("%s/%s is shared with run-b and must survive", kept[0], kept[1])
		}
	}
	if len(h.forge.Watchers) != 1 || len(h.index.Artifacts) != 1 {
		t.Error("the shared watcher and artifact must survive")
	}
	if run := h.mustRun("run-b"); run.Status.Phase != wizardv1.RunReady {
		t.Errorf("run-b must be unaffected, phase=%s", run.Status.Phase)
	}

	if err := h.c.Delete(context.Background(), h.mustRun("run-b")); err != nil {
		t.Fatal(err)
	}
	h.reconcile("run-b")
	if len(h.weave.Objs) != 0 || len(h.forge.Watchers) != 0 || len(h.index.Artifacts) != 0 || h.ledgerSize() != 0 {
		t.Errorf("the last run must clean up everything: weave=%d forge=%d index=%d ledger=%d", len(h.weave.Objs), len(h.forge.Watchers), len(h.index.Artifacts), h.ledgerSize())
	}
}

// ---- pure helpers ----

func TestRetryDelay(t *testing.T) {
	want := map[int32]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 5: 32 * time.Second, 6: time.Minute, 50: time.Minute}
	for n, d := range want {
		if got := retryDelay(n); got != d {
			t.Errorf("retryDelay(%d) = %v, want %v", n, got, d)
		}
	}
}

func TestRunChangedIgnoresStatusOnlyUpdates(t *testing.T) {
	p := runChanged()
	mk := func(gen int64, ann map[string]string) *wizardv1.WizardRun {
		return &wizardv1.WizardRun{ObjectMeta: metav1.ObjectMeta{Generation: gen, Annotations: ann}}
	}
	statusOnly := mk(1, nil)
	statusOnly2 := mk(1, nil)
	statusOnly2.Status.Phase = wizardv1.RunRunning
	if p.Update(event.UpdateEvent{ObjectOld: statusOnly, ObjectNew: statusOnly2}) {
		t.Error("a status-only update must not retrigger the reconciler")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: mk(1, nil), ObjectNew: mk(2, nil)}) {
		t.Error("a spec change (generation) must trigger")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: mk(1, nil), ObjectNew: mk(1, map[string]string{wizardv1.AnnotationRetry: "true"})}) {
		t.Error("the retry annotation must trigger")
	}
	deleting := mk(1, nil)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if !p.Update(event.UpdateEvent{ObjectOld: mk(1, nil), ObjectNew: deleting}) {
		t.Error("deletion must trigger")
	}
}

func TestSweeperFinishesInterruptedDeletions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createDefinition("python-job", pythonDefinition())
	h.forgeHasBuilt()
	h.createRun("run-a", nightlyParams, "main.py")
	h.reconcile("run-a")

	// A rollback dies after tombstoning the watcher entry but before deleting anything.
	key := ledgerKey("forge", steps.KindGitWatcher, "nightly")
	if out, _, err := h.led.Release(ctx, key, wizardv1.ResourceReference{Run: "run-a", Step: "watcher"}); err != nil || out != ledger.OutcomeDelete {
		t.Fatalf("release: %v %v", out, err)
	}

	sw := &Sweeper{Envs: h.envs, Interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- sw.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for {
		if e, _ := h.led.Get(context.Background(), key); e == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the sweeper did not finish the interrupted deletion")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start must return cleanly on cancel, got %v", err)
	}
	if _, ok := h.forge.Watchers["nightly"]; ok {
		t.Error("the sweeper must also delete the watcher upstream")
	}
	if !sw.NeedLeaderElection() {
		t.Error("only the leader should sweep")
	}
}
