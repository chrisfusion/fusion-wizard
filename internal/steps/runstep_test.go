// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"strings"
	"testing"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func imageTplParams() map[string]string {
	return map[string]string{"name": "base", "port": "8080"}
}

func runParams(name, image string) map[string]string {
	return map[string]string{"name": name, "chain": "base", "image": image, "ingressName": name}
}

func TestServiceTemplateImageOnly(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepServiceTemplate, "run-a", "template", imageTplParams())

	spec := r.weave.Objs["servicetemplates/base"]["spec"].(map[string]any)
	if _, has := spec["codeSource"]; has {
		t.Errorf("an image-only template must have no codeSource: %v", spec)
	}
	if spec["image"] != "registry.example/runner:1.0.0" {
		t.Errorf("image = %v", spec["image"])
	}
	if _, has := spec["ingress"]; has {
		t.Errorf("the shared template must not carry an ingress (it is per run): %v", spec)
	}

	// A second wizard run with the same settings shares it.
	res := r.mustDone(t, wizardv1.StepServiceTemplate, "run-b", "template", imageTplParams())
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted || r.log.Count("create weave/servicetemplates/") != 1 {
		t.Errorf("run-b must adopt: %+v (events %v)", res.Resources, r.log.Snapshot())
	}
	// Another port under the same name is a visible conflict, not a silent change.
	p := imageTplParams()
	p["port"] = "9090"
	if _, err := r.ensure(t, wizardv1.StepServiceTemplate, "run-c", "template", p); !IsPermanent(err) {
		t.Errorf("a different port must conflict, got %v", err)
	}
	// An artifact template of the same name is a different thing.
	art := imageTplParams()
	art["artifactName"], art["tag"] = "app.x", "stable"
	if _, err := r.ensure(t, wizardv1.StepServiceTemplate, "run-d", "template", art); !IsPermanent(err) {
		t.Errorf("an artifact template must conflict with the image-only one, got %v", err)
	}
}

func TestServiceTemplateArtifactAndTagComeTogether(t *testing.T) {
	r := newRig(t)
	for _, extra := range []map[string]string{{"artifactName": "app.x"}, {"tag": "stable"}} {
		p := imageTplParams()
		for k, v := range extra {
			p[k] = v
		}
		if _, err := r.ensure(t, wizardv1.StepServiceTemplate, "run-a", "template", p); !IsPermanent(err) {
			t.Errorf("%v: want a permanent error, got %v", extra, err)
		}
	}
	if r.weave.Has("servicetemplates", "base") {
		t.Error("an invalid template must not be created")
	}
}

func TestRunStepSpecAndSharedRollback(t *testing.T) {
	r := newRig(t)
	deploy := func(run, svc, image string) {
		r.mustDone(t, wizardv1.StepServiceTemplate, run, "template", imageTplParams())
		r.mustDone(t, wizardv1.StepChain, run, "chain", map[string]string{"name": "base", "serviceTemplate": "base"})
		r.mustDone(t, wizardv1.StepRun, run, "run", runParams(svc, image))
	}
	deploy("run-a", "cust-a", "registry.example/a/app:1.4.2")
	deploy("run-b", "cust-b", "registry.example/b/app:2.0.0")

	if n := r.log.Count("create weave/servicetemplates/") + r.log.Count("create weave/chains/"); n != 2 {
		t.Errorf("n services must share one template and one chain, got %d creates (events %v)", n, r.log.Snapshot())
	}
	if n := r.log.Count("create weave/runs/"); n != 2 {
		t.Errorf("want one run per service, got %d", n)
	}

	obj := r.weave.Objs["runs/cust-a"]
	spec := obj["spec"].(map[string]any)
	if spec["chainRef"].(map[string]any)["name"] != "base" {
		t.Errorf("chainRef = %v", spec["chainRef"])
	}
	so := spec["stepOverrides"].([]any)[0].(map[string]any)
	if so["stepName"] != "run" || so["ingressName"] != "cust-a" {
		t.Errorf("stepOverrides = %v", so)
	}
	if _, has := so["artifactName"]; has {
		t.Errorf("the override must be image-only (no artifact/tag): %v", so)
	}
	io := spec["imageOverrides"].([]any)[0].(map[string]any)
	if io["stepName"] != "run" || io["image"] != "registry.example/a/app:1.4.2" {
		t.Errorf("imageOverrides = %v", io)
	}
	labels := obj["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels[ledger.LabelManagedBy] != ledger.ManagedByWizard || labels[ledger.LabelRun] != "run-a" {
		t.Errorf("owner labels = %v", labels)
	}

	// Rolling back one service removes only its run; the other keeps the shared template and chain.
	if err := r.env.Rollback(context.Background(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if r.weave.Has("runs", "cust-a") || !r.weave.Has("runs", "cust-b") || !r.weave.Has("chains", "base") || !r.weave.Has("servicetemplates", "base") {
		t.Errorf("rollback of run-a must only delete its run: %v", r.log.Snapshot())
	}
	before := len(r.log.Snapshot())
	if err := r.env.Rollback(context.Background(), "run-b"); err != nil {
		t.Fatal(err)
	}
	want := []string{"delete weave/runs/cust-b", "delete weave/chains/base", "delete weave/servicetemplates/base"}
	var got []string
	for _, e := range r.log.Snapshot()[before:] {
		if strings.HasPrefix(e, "delete ") {
			got = append(got, e)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("last rollback order:\n got %v\nwant %v", got, want)
	}
}

func TestRunStepConflictsAndAdoption(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepRun, "run-a", "run", runParams("cust", "registry.example/a/app:1"))

	res := r.mustDone(t, wizardv1.StepRun, "run-a", "run", runParams("cust", "registry.example/a/app:1"))
	if r.log.Count("create weave/runs/") != 1 {
		t.Errorf("re-confirming must not recreate: %v (%+v)", r.log.Snapshot(), res.Resources)
	}
	// Same run name for another image or ingress is a conflict, never a silent update.
	if _, err := r.ensure(t, wizardv1.StepRun, "run-b", "run", runParams("cust", "registry.example/a/app:2")); !IsPermanent(err) {
		t.Errorf("a different image must conflict, got %v", err)
	}
	p := runParams("cust", "registry.example/a/app:1")
	p["ingressName"] = "other"
	if _, err := r.ensure(t, wizardv1.StepRun, "run-b", "run", p); !IsPermanent(err) {
		t.Errorf("a different ingress must conflict, got %v", err)
	}
	// A hand-made run with the right content is accepted as unowned and never deleted.
	r.weave.Seed("runs", "mine", map[string]any{
		"chainRef":       map[string]any{"name": "base"},
		"stepOverrides":  []any{map[string]any{"stepName": "run", "ingressName": "mine"}},
		"imageOverrides": []any{map[string]any{"stepName": "run", "image": "registry.example/m/app:1"}},
	})
	res = r.mustDone(t, wizardv1.StepRun, "run-c", "run", runParams("mine", "registry.example/m/app:1"))
	if res.Resources[0].Disposition != wizardv1.DispositionFoundUnowned {
		t.Errorf("disposition = %v", res.Resources[0].Disposition)
	}
	// ...but one that runs a different image must conflict: the wizard has no ledger entry to compare.
	r.weave.Seed("runs", "theirs", map[string]any{
		"chainRef":       map[string]any{"name": "base"},
		"stepOverrides":  []any{map[string]any{"stepName": "run", "ingressName": "theirs"}},
		"imageOverrides": []any{map[string]any{"stepName": "run", "image": "registry.example/t/app:1"}},
	})
	if _, err := r.ensure(t, wizardv1.StepRun, "run-d", "run", runParams("theirs", "registry.example/t/app:2")); !IsPermanent(err) {
		t.Errorf("a hand-made run with another image must conflict, got %v", err)
	}
	if err := r.env.Rollback(context.Background(), "run-c"); err != nil || !r.weave.Has("runs", "mine") {
		t.Errorf("an unowned run must survive rollback (err %v)", err)
	}
}

func TestImageParamValidation(t *testing.T) {
	good := []string{"registry.example/a/app:1.4.2", "localhost:5000/app:v1", "registry.example/a/app@sha256:abc123", "app:1"}
	bad := []string{"", "registry.example/a/app", "registry.example/a/app:latest", "registry.example/a/app:", "localhost:5000/app", "reg/app:1 x"}
	for _, img := range good {
		if err := ValidateImageParams(wizardv1.StepRun, map[string]string{"image": img}); err != nil {
			t.Errorf("%q must be accepted, got %v", img, err)
		}
	}
	for _, img := range bad {
		if err := ValidateImageParams(wizardv1.StepRun, map[string]string{"image": img}); !IsPermanent(err) {
			t.Errorf("%q must be rejected, got %v", img, err)
		}
	}
	if err := ValidateImageParams(wizardv1.StepRun, map[string]string{"image": "app:1", "imagePullPolicy": "Sometimes"}); !IsPermanent(err) {
		t.Errorf("a bad pull policy must be rejected, got %v", err)
	}
	// Other step types carry an image param with no tag rule (the instance runner image default).
	if err := ValidateImageParams(wizardv1.StepServiceTemplate, map[string]string{"image": "runner"}); err != nil {
		t.Errorf("only the run step is checked, got %v", err)
	}

	r := newRig(t)
	if _, err := r.ensure(t, wizardv1.StepRun, "run-a", "run", runParams("cust", "registry.example/a/app:latest")); !IsPermanent(err) {
		t.Errorf("the step itself must reject :latest, got %v", err)
	}
	if r.weave.Has("runs", "cust") {
		t.Error("an invalid run must not be created")
	}
}

func TestJobTemplateImageOnly(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "base"}
	r.mustDone(t, wizardv1.StepJobTemplate, "run-a", "template", p)

	spec := r.weave.Objs["jobtemplates/base"]["spec"].(map[string]any)
	if _, has := spec["codeSource"]; has {
		t.Errorf("an image-only job template must have no codeSource: %v", spec)
	}
	res := r.mustDone(t, wizardv1.StepJobTemplate, "run-b", "template", p)
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted || r.log.Count("create weave/jobtemplates/") != 1 {
		t.Errorf("run-b must adopt: %+v (events %v)", res.Resources, r.log.Snapshot())
	}
	// An artifact template of the same name is a different thing.
	if _, err := r.ensure(t, wizardv1.StepJobTemplate, "run-c", "template", map[string]string{"name": "base", "artifactName": "app.x", "tag": "stable"}); !IsPermanent(err) {
		t.Errorf("an artifact template must conflict with the image-only one, got %v", err)
	}
	for _, half := range []map[string]string{{"artifactName": "app.x"}, {"tag": "stable"}} {
		q := map[string]string{"name": "other"}
		for k, v := range half {
			q[k] = v
		}
		if _, err := r.ensure(t, wizardv1.StepJobTemplate, "run-d", "template", q); !IsPermanent(err) {
			t.Errorf("%v: want a permanent error, got %v", half, err)
		}
	}
	if r.weave.Has("jobtemplates", "other") {
		t.Error("an invalid template must not be created")
	}
}

func TestRunStepJobMode(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "job-a", "chain": "base", "image": "registry.example/a/job:1", "stepKind": "Job"}
	r.mustDone(t, wizardv1.StepRun, "run-a", "run", p)

	spec := r.weave.Objs["runs/job-a"]["spec"].(map[string]any)
	if _, has := spec["stepOverrides"]; has {
		t.Errorf("a Job step is not run-owned, no stepOverrides allowed: %v", spec)
	}
	io := spec["imageOverrides"].([]any)[0].(map[string]any)
	if io["stepName"] != "run" || io["image"] != "registry.example/a/job:1" {
		t.Errorf("imageOverrides = %v", io)
	}

	bad := map[string]string{"name": "job-b", "chain": "base", "image": "registry.example/a/job:1", "stepKind": "Job", "ingressName": "x"}
	if _, err := r.ensure(t, wizardv1.StepRun, "run-b", "run", bad); !IsPermanent(err) {
		t.Errorf("a Job run has no ingress, got %v", err)
	}
	bad = map[string]string{"name": "job-c", "chain": "base", "image": "registry.example/a/job:1", "stepKind": "Cron"}
	if _, err := r.ensure(t, wizardv1.StepRun, "run-b", "run", bad); !IsPermanent(err) {
		t.Errorf("an unknown stepKind must be rejected, got %v", err)
	}
	// The same run name as a Deploy run is a conflict, never a silent reuse.
	dep := map[string]string{"name": "job-a", "chain": "base", "image": "registry.example/a/job:1"}
	if _, err := r.ensure(t, wizardv1.StepRun, "run-c", "run", dep); !IsPermanent(err) {
		t.Errorf("a Deploy run must not adopt a Job run, got %v", err)
	}
	// A hand-made Deploy run is not accepted for a Job step either (no ledger entry to catch it).
	r.weave.Seed("runs", "hand", map[string]any{
		"chainRef":       map[string]any{"name": "base"},
		"stepOverrides":  []any{map[string]any{"stepName": "run"}},
		"imageOverrides": []any{map[string]any{"stepName": "run", "image": "registry.example/h/job:1"}},
	})
	hand := map[string]string{"name": "hand", "chain": "base", "image": "registry.example/h/job:1", "stepKind": "Job"}
	if _, err := r.ensure(t, wizardv1.StepRun, "run-d", "run", hand); !IsPermanent(err) {
		t.Errorf("a hand-made Deploy run must conflict with a Job step, got %v", err)
	}
}

func TestTriggerImageOverride(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "tr", "chain": "base", "type": "Cron", "schedule": "0 9 * * *",
		"image": "registry.example/a/job:1", "imagePullPolicy": "IfNotPresent"}
	r.mustDone(t, wizardv1.StepTrigger, "run-a", "trigger", p)

	io := r.weave.Objs["triggers/tr"]["spec"].(map[string]any)["imageOverrides"].([]any)[0].(map[string]any)
	if io["stepName"] != "run" || io["image"] != "registry.example/a/job:1" || io["imagePullPolicy"] != "IfNotPresent" {
		t.Errorf("imageOverrides = %v", io)
	}

	// Without an image there is no override at all, and the old hash stays what it was.
	r.mustDone(t, wizardv1.StepTrigger, "run-a", "plain", map[string]string{"name": "plain", "chain": "base"})
	if _, has := r.weave.Objs["triggers/plain"]["spec"].(map[string]any)["imageOverrides"]; has {
		t.Error("a trigger without an image must not carry imageOverrides")
	}

	// Same trigger name for another image is a conflict, also for a hand-made trigger.
	other := map[string]string{"name": "tr", "chain": "base", "type": "Cron", "schedule": "0 9 * * *", "image": "registry.example/a/job:2", "imagePullPolicy": "IfNotPresent"}
	if _, err := r.ensure(t, wizardv1.StepTrigger, "run-b", "trigger", other); !IsPermanent(err) {
		t.Errorf("a different image must conflict, got %v", err)
	}
	r.weave.Seed("triggers", "hand", map[string]any{
		"chainRef": map[string]any{"name": "base"}, "type": "Cron", "schedule": "0 9 * * *",
		"imageOverrides": []any{map[string]any{"stepName": "run", "image": "registry.example/h/job:1"}},
	})
	hand := map[string]string{"name": "hand", "chain": "base", "type": "Cron", "schedule": "0 9 * * *", "image": "registry.example/h/job:2"}
	if _, err := r.ensure(t, wizardv1.StepTrigger, "run-c", "trigger", hand); !IsPermanent(err) {
		t.Errorf("a hand-made trigger with another image must conflict, got %v", err)
	}

	for _, bad := range []map[string]string{
		{"name": "x", "chain": "base", "image": "registry.example/a/job:latest"},
		{"name": "x", "chain": "base", "image": "registry.example/a/job"},
		{"name": "x", "chain": "base", "imagePullPolicy": "Always"},
	} {
		if _, err := r.ensure(t, wizardv1.StepTrigger, "run-d", "trigger", bad); !IsPermanent(err) {
			t.Errorf("%v: want a permanent error, got %v", bad, err)
		}
	}
	if r.weave.Has("triggers", "x") {
		t.Error("an invalid trigger must not be created")
	}
}
