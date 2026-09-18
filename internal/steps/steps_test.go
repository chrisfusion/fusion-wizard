// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const repoURL = "https://git.example/team/nightly.git"

func gwParams(name, repo string) map[string]string {
	return map[string]string{"name": name, "repoUrl": repo}
}

func TestIsPermanent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"explicit", permanentf("bad input"), true},
		{"wrapped explicit", errors.Join(errors.New("x"), permanentf("bad")), true},
		{"http 404", stepstest.APIErr(404), true},
		{"http 422", stepstest.APIErr(422), true},
		{"http 503", stepstest.APIErr(503), false},
		{"http 429", stepstest.APIErr(429), false},
		{"no response", &upstream.APIError{Status: 0}, false},
		{"unclassified", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := IsPermanent(c.err); got != c.want {
			t.Errorf("%s: IsPermanent = %v, want %v", c.name, got, c.want)
		}
	}
}

// ---- gitWatcher ----

func TestGitWatcherCreateAdoptAndConflict(t *testing.T) {
	r := newRig(t)

	res := r.mustDone(t, wizardv1.StepGitWatcher, "run-a", "watcher", gwParams("nightly", repoURL))
	ref := res.Resources[0]
	if ref.Service != wizardv1.ServiceForge || ref.Kind != KindGitWatcher || ref.Name != "nightly" || ref.Disposition != wizardv1.DispositionCreated {
		t.Errorf("first run ref = %+v", ref)
	}
	if e := r.entry(t, wizardv1.ServiceForge, KindGitWatcher, "nightly"); e == nil || !e.Spec.Managed || e.Spec.SpecHash == "" || len(e.Spec.Refs) != 1 {
		t.Fatalf("ledger entry = %+v", e)
	}

	res = r.mustDone(t, wizardv1.StepGitWatcher, "run-b", "watcher", gwParams("nightly", repoURL))
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("second run must adopt, got %v", res.Resources[0].Disposition)
	}
	if n := r.log.Count("create forge/gitwatcher/"); n != 1 {
		t.Errorf("forge watcher created %d times, want 1", n)
	}
	r.mustDone(t, wizardv1.StepGitWatcher, "run-b", "watcher", gwParams("nightly", repoURL)) // resume: idempotent
	if e := r.entry(t, wizardv1.ServiceForge, KindGitWatcher, "nightly"); len(e.Spec.Refs) != 2 {
		t.Errorf("refs after an idempotent repeat = %d, want 2", len(e.Spec.Refs))
	}

	// A different repository under the same name is a visible conflict, not a silent reuse.
	_, err := r.ensure(t, wizardv1.StepGitWatcher, "run-c", "watcher", gwParams("nightly", "https://git.example/other.git"))
	if !IsPermanent(err) || !strings.Contains(err.Error(), "different settings") {
		t.Errorf("different repo: err = %v", err)
	}
	// The same repository with another ref is caught through the ledger hash.
	p := gwParams("nightly", repoURL)
	p["repoRef"] = "release"
	_, err = r.ensure(t, wizardv1.StepGitWatcher, "run-c", "watcher", p)
	if !IsPermanent(err) || !strings.Contains(err.Error(), "another wizard run") {
		t.Errorf("different ref: err = %v", err)
	}
	if e := r.entry(t, wizardv1.ServiceForge, KindGitWatcher, "nightly"); len(e.Spec.Refs) != 2 {
		t.Errorf("a failed ensure must not add a reference (refs=%d)", len(e.Spec.Refs))
	}
}

func TestFoundUnownedResourceIsNeverDeleted(t *testing.T) {
	r := newRig(t)
	r.forge.Watchers["hand-made"] = &upstream.GitWatcher{Name: "hand-made", Spec: upstream.GitWatcherSpec{RepoURL: repoURL, BuildType: "app"}}

	res := r.mustDone(t, wizardv1.StepGitWatcher, "run-a", "watcher", gwParams("hand-made", repoURL))
	if res.Resources[0].Disposition != wizardv1.DispositionFoundUnowned {
		t.Errorf("disposition = %v", res.Resources[0].Disposition)
	}
	if e := r.entry(t, wizardv1.ServiceForge, KindGitWatcher, "hand-made"); e.Spec.Managed {
		t.Error("a resource found without a ledger entry must be recorded unmanaged")
	}
	if n := r.log.Count("create forge/gitwatcher/"); n != 0 {
		t.Errorf("nothing should have been created, got %d", n)
	}

	if err := r.env.Rollback(context.Background(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if n := r.log.Count("delete forge/"); n != 0 {
		t.Error("rollback deleted a resource the wizard did not create")
	}
	if e := r.entry(t, wizardv1.ServiceForge, KindGitWatcher, "hand-made"); e != nil {
		t.Error("the unmanaged ledger entry must go away with its last reference")
	}

	// A hand-made watcher for a different repo is a conflict.
	r.forge.Watchers["other"] = &upstream.GitWatcher{Name: "other", Spec: upstream.GitWatcherSpec{RepoURL: "https://x/y.git", BuildType: "app"}}
	if _, err := r.ensure(t, wizardv1.StepGitWatcher, "run-a", "watcher", gwParams("other", repoURL)); !IsPermanent(err) {
		t.Errorf("mismatching hand-made watcher: err = %v", err)
	}
}

// ---- waitBuild ----

func newWaitRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	for _, n := range []string{"wa", "wb"} {
		r.forge.Watchers[n] = &upstream.GitWatcher{Name: n, Spec: upstream.GitWatcherSpec{RepoURL: repoURL, BuildType: "app"}, Status: upstream.GitWatcherStatus{Phase: "Active"}}
	}
	return r
}

func build(id int64, status string) upstream.Build {
	u := repoURL
	return upstream.Build{ID: id, Name: "nightly", Version: "1.0.0", Status: status, BuildType: "app", RepoURL: &u}
}

func succeeded(id int64) upstream.Build {
	b := build(id, "SUCCESS")
	art, ver := int64(42), "1.0.0"
	b.IndexArtifactID, b.IndexArtifactVersion = &art, &ver
	return b
}

func (r *rig) wait(t *testing.T, in Input) (Result, error) {
	t.Helper()
	step, _ := Lookup(wizardv1.StepWaitBuild)
	if in.Params == nil {
		in.Params = map[string]string{"watcher": "wa"}
	}
	if in.Run == "" {
		in.Run, in.Key = "run-a", "build"
	}
	return step.Ensure(context.Background(), r.env, in)
}

func TestWaitBuildPollingToSuccess(t *testing.T) {
	r := newWaitRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0")

	res, err := r.wait(t, Input{})
	if err != nil || res.Done || res.Requeue != r.env.Cfg.PollInterval || !strings.Contains(res.Message, "start a build") {
		t.Fatalf("no build yet: %+v, %v", res, err)
	}

	r.forge.SetBuild(build(1, "BUILDING"))
	res, err = r.wait(t, Input{})
	if err != nil || res.Done || res.Outputs["buildId"] != "1" || !strings.Contains(res.Message, "BUILDING") {
		t.Fatalf("building: %+v, %v", res, err)
	}
	prior := res.Outputs // the reconciler persists these between polls

	r.forge.SetBuild(succeeded(1))
	res, err = r.wait(t, Input{Prior: prior})
	if err != nil || !res.Done {
		t.Fatalf("success: %+v, %v", res, err)
	}
	want := map[string]string{"buildId": "1", "name": "nightly", "artifactName": "app.nightly", "artifactId": "42", "version": "1.0.0"}
	for k, v := range want {
		if res.Outputs[k] != v {
			t.Errorf("output %s = %q, want %q", k, res.Outputs[k], v)
		}
	}
	e := r.entry(t, wizardv1.ServiceIndex, KindArtifact, "app.nightly")
	if e == nil || !e.Spec.Managed || e.Spec.ExternalID != "42" {
		t.Fatalf("the wizard must claim the artifact forge built: %+v", e)
	}
	if res.Resources[0].Disposition != wizardv1.DispositionCreated {
		t.Errorf("claimed artifact disposition = %v", res.Resources[0].Disposition)
	}
}

func TestWaitBuildFailures(t *testing.T) {
	r := newWaitRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0")

	r.forge.SetBuild(build(1, "FAILED"))
	if _, err := r.wait(t, Input{}); !IsPermanent(err) || !strings.Contains(err.Error(), "failed") {
		t.Errorf("failed build: %v", err)
	}

	r.forge.SetBuild(build(1, "BUILDING"))
	r.forge.SetWatcherStatus("wa", upstream.GitWatcherStatus{Phase: "Disabled", ConsecutiveFailures: 2, LastError: "pip exploded"})
	if _, err := r.wait(t, Input{}); !IsPermanent(err) || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "pip exploded") {
		t.Errorf("disabled watcher: %v", err)
	}
	r.forge.SetWatcherStatus("wa", upstream.GitWatcherStatus{Phase: "Active"})

	if res, err := r.wait(t, Input{StartedAt: r.now.Add(-9 * time.Minute)}); err != nil || res.Done {
		t.Errorf("9 min into a 10 min budget must still wait: %+v %v", res, err)
	}
	if _, err := r.wait(t, Input{StartedAt: r.now.Add(-11 * time.Minute)}); !IsPermanent(err) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: %v", err)
	}

	noArtifact := build(1, "SUCCESS")
	r.forge.SetBuild(noArtifact)
	if _, err := r.wait(t, Input{}); !IsPermanent(err) || !strings.Contains(err.Error(), "no index artifact") {
		t.Errorf("success without artifact: %v", err)
	}

	// Forge skips a version it built before, even if the artifact was deleted since.
	delete(r.index.Artifacts, "app.nightly")
	r.forge.SetBuild(succeeded(1))
	if _, err := r.wait(t, Input{}); !IsPermanent(err) || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("artifact gone from the index: %v", err)
	}
}

func TestWaitBuildFindsTheRightBuild(t *testing.T) {
	r := newWaitRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0")

	// A newer build of ANOTHER repository, and one of ours in another subfolder, must be ignored.
	other := build(9, "BUILDING")
	x := "https://git.example/other.git"
	other.RepoURL = &x
	r.forge.SetBuild(other)
	sub := build(8, "BUILDING")
	dir := "subdir"
	sub.ProjectDir = &dir
	r.forge.SetBuild(sub)
	if res, err := r.wait(t, Input{}); err != nil || res.Done || res.Outputs != nil {
		t.Fatalf("foreign builds must not match: %+v, %v", res, err)
	}

	r.forge.SetBuild(succeeded(3))
	// Forge deleted the row the step remembered (it does this when it retries a failed build):
	// fall back to discovery instead of failing.
	res, err := r.wait(t, Input{Prior: map[string]string{"buildId": "99"}})
	if err != nil || !res.Done || res.Outputs["buildId"] != "3" {
		t.Fatalf("vanished remembered build: %+v, %v", res, err)
	}
}

func TestWaitBuildSharedArtifactRollback(t *testing.T) {
	r := newWaitRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0")
	r.forge.SetBuild(succeeded(1))

	resA, err := r.wait(t, Input{Run: "run-a", Key: "build", Params: map[string]string{"watcher": "wa"}})
	if err != nil || !resA.Done {
		t.Fatal(resA, err)
	}
	resB, err := r.wait(t, Input{Run: "run-b", Key: "build", Params: map[string]string{"watcher": "wb"}})
	if err != nil || resB.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Fatalf("second run must adopt the artifact: %+v, %v", resB, err)
	}

	ctx := context.Background()
	if err := r.env.Rollback(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if r.log.Count("delete index/artifact/") != 0 {
		t.Fatal("the artifact is still used by run-b and must survive run-a's rollback")
	}
	if err := r.env.Rollback(ctx, "run-b"); err != nil {
		t.Fatal(err)
	}
	if r.log.Count("delete index/artifact/42") != 1 {
		t.Errorf("events: %v", r.log.Snapshot())
	}
}

// ---- tag ----

func tagParams(version string) map[string]string {
	return map[string]string{"artifactId": "42", "artifactName": "app.nightly", "version": version}
}

func TestTagCreateAdoptAndRollback(t *testing.T) {
	r := newRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0", "1.1.0")

	res := r.mustDone(t, wizardv1.StepTag, "run-a", "tag", tagParams("1.0.0"))
	if res.Outputs["tag"] != "stable" || res.Resources[0].Disposition != wizardv1.DispositionCreated {
		t.Errorf("first run: %+v", res)
	}
	if e := r.entry(t, wizardv1.ServiceIndex, KindTag, "app.nightly:stable"); e == nil || !e.Spec.Managed || e.Spec.ExternalID != "42" {
		t.Fatalf("entry = %+v", e)
	}

	// A second run adopts the tag and re-points it to its own version.
	res = r.mustDone(t, wizardv1.StepTag, "run-b", "tag", tagParams("1.1.0"))
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("second run disposition = %v", res.Resources[0].Disposition)
	}
	if r.log.IndexOf("settag index/42/stable=1.1.0") < 0 {
		t.Errorf("the adopted tag must be re-pointed: %v", r.log.Snapshot())
	}

	ctx := context.Background()
	_ = r.env.Rollback(ctx, "run-a")
	if r.log.Count("delete index/tag/") != 0 {
		t.Fatal("shared tag deleted while run-b still uses it")
	}
	_ = r.env.Rollback(ctx, "run-b")
	if r.log.Count("delete index/tag/42/stable") != 1 {
		t.Errorf("events: %v", r.log.Snapshot())
	}
}

func TestTagFoundUnownedAndErrors(t *testing.T) {
	r := newRig(t)
	r.index.AddArtifact(42, "app.nightly", "1.0.0")
	if err := r.index.SetTag(context.Background(), 42, "stable", "1.0.0"); err != nil { // someone else's tag
		t.Fatal(err)
	}

	res := r.mustDone(t, wizardv1.StepTag, "run-a", "tag", tagParams("1.0.0"))
	if res.Resources[0].Disposition != wizardv1.DispositionFoundUnowned {
		t.Errorf("disposition = %v", res.Resources[0].Disposition)
	}
	_ = r.env.Rollback(context.Background(), "run-a")
	if r.log.Count("delete index/tag/") != 0 {
		t.Error("a tag the wizard did not create must survive rollback")
	}

	p := tagParams("1.0.0")
	p["artifactId"] = "abc"
	if _, err := r.ensure(t, wizardv1.StepTag, "run-a", "tag", p); !IsPermanent(err) {
		t.Errorf("non-numeric artifactId: %v", err)
	}
	p["artifactId"] = "7" // artifact does not exist
	if _, err := r.ensure(t, wizardv1.StepTag, "run-a", "tag", p); !IsPermanent(err) {
		t.Errorf("unknown artifact: %v", err)
	}
	p = tagParams("1.0.0")
	p["tag"] = "beta"
	if res := r.mustDone(t, wizardv1.StepTag, "run-a", "tag", p); res.Outputs["tag"] != "beta" {
		t.Errorf("explicit tag param ignored: %v", res.Outputs)
	}
}

// ---- jobTemplate ----

func tplParams(artifact string) map[string]string {
	return map[string]string{"name": "tpl", "artifactName": artifact, "tag": "stable"}
}

func TestJobTemplateSpecAndSharing(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepJobTemplate, "run-a", "template", tplParams("app.nightly"))

	spec := r.weave.Objs["jobtemplates/tpl"]["spec"].(map[string]any)
	if spec["image"] != "registry.example/runner:1.0.0" {
		t.Errorf("image must default to the instance runner image, got %v", spec["image"])
	}
	cs := spec["codeSource"].(map[string]any)
	if cs["artifactName"] != "app.nightly" || cs["tag"] != "stable" {
		t.Errorf("codeSource = %v", cs)
	}
	limits := spec["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["memory"] != "1Gi" {
		t.Errorf("default resources not applied: %v", spec["resources"])
	}

	// Instance defaults are not part of a template's identity: changing them must not turn an
	// existing shared template into a conflict for the next run.
	r.env.Cfg.DefaultResources = corev1.ResourceRequirements{}
	r.env.Cfg.RunnerImage = "registry.example/runner:2.0.0"
	res := r.mustDone(t, wizardv1.StepJobTemplate, "run-b", "template", tplParams("app.nightly"))
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted || r.log.Count("create weave/jobtemplates/") != 1 {
		t.Errorf("run-b must adopt: %+v (events %v)", res.Resources, r.log.Snapshot())
	}

	if _, err := r.ensure(t, wizardv1.StepJobTemplate, "run-c", "template", tplParams("app.other")); !IsPermanent(err) {
		t.Errorf("same template name for another artifact must conflict, got %v", err)
	}

	p := tplParams("app.nightly")
	p["name"] = "tpl2"
	p["image"] = "custom:1"
	r.mustDone(t, wizardv1.StepJobTemplate, "run-a", "template2", p)
	if got := r.weave.Objs["jobtemplates/tpl2"]["spec"].(map[string]any)["image"]; got != "custom:1" {
		t.Errorf("explicit image param ignored: %v", got)
	}
}

// ---- chain ----

func TestChainSpecAndConflict(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepChain, "run-a", "chain", map[string]string{"name": "ch", "jobTemplate": "tpl"})

	steps := r.weave.Objs["chains/ch"]["spec"].(map[string]any)["steps"].([]any)
	st := steps[0].(map[string]any)
	if len(steps) != 1 || st["name"] != "run" || st["stepKind"] != "Job" || st["jobTemplateRef"].(map[string]any)["name"] != "tpl" {
		t.Errorf("chain steps = %v", steps)
	}

	r.mustDone(t, wizardv1.StepChain, "run-a", "chain2", map[string]string{"name": "ch2", "jobTemplate": "tpl", "stepName": "main"})
	if got := r.weave.Objs["chains/ch2"]["spec"].(map[string]any)["steps"].([]any)[0].(map[string]any)["name"]; got != "main" {
		t.Errorf("stepName param ignored: %v", got)
	}

	// Same chain name for another template: conflict.
	if _, err := r.ensure(t, wizardv1.StepChain, "run-b", "chain", map[string]string{"name": "ch", "jobTemplate": "other"}); !IsPermanent(err) {
		t.Errorf("err = %v", err)
	}
	// A hand-made chain that runs the template is accepted as unowned; one that does not is a conflict.
	r.weave.Seed("chains", "mine", map[string]any{"steps": []any{map[string]any{"name": "x", "jobTemplateRef": map[string]any{"name": "tpl"}}}})
	res := r.mustDone(t, wizardv1.StepChain, "run-a", "chain3", map[string]string{"name": "mine", "jobTemplate": "tpl"})
	if res.Resources[0].Disposition != wizardv1.DispositionFoundUnowned {
		t.Errorf("disposition = %v", res.Resources[0].Disposition)
	}
	r.weave.Seed("chains", "theirs", map[string]any{"steps": []any{}})
	if _, err := r.ensure(t, wizardv1.StepChain, "run-a", "chain4", map[string]string{"name": "theirs", "jobTemplate": "tpl"}); !IsPermanent(err) {
		t.Errorf("err = %v", err)
	}
}

// ---- trigger ----

func TestTriggerSpecs(t *testing.T) {
	r := newRig(t)

	r.mustDone(t, wizardv1.StepTrigger, "run-a", "trigger/main.py", map[string]string{
		"name": "nightly-main", "chain": "ch", "override.ENTRYPOINT": "main.py", "override.ALPHA": "1",
	})
	spec := r.weave.Objs["triggers/nightly-main"]["spec"].(map[string]any)
	if spec["type"] != "OnDemand" || spec["chainRef"].(map[string]any)["name"] != "ch" {
		t.Errorf("spec = %v", spec)
	}
	if _, has := spec["schedule"]; has {
		t.Error("an OnDemand trigger must not carry a schedule")
	}
	ov := spec["parameterOverrides"].([]any)
	if len(ov) != 2 || ov[0].(map[string]any)["name"] != "ALPHA" || ov[1].(map[string]any)["name"] != "ENTRYPOINT" || ov[1].(map[string]any)["value"] != "main.py" {
		t.Errorf("overrides must be name/value pairs in a stable order: %v", ov)
	}

	r.mustDone(t, wizardv1.StepTrigger, "run-a", "trigger/cron", map[string]string{
		"name": "cron-t", "chain": "ch", "type": "Cron", "schedule": "0 */5 * * * *",
	})
	if s := r.weave.Objs["triggers/cron-t"]["spec"].(map[string]any); s["type"] != "Cron" || s["schedule"] != "0 */5 * * * *" {
		t.Errorf("cron spec = %v", s)
	}

	bad := []map[string]string{
		{"name": "t", "chain": "ch", "type": "Kafka"},
		{"name": "t", "chain": "ch", "type": "Cron"},
		{"name": "t", "chain": "ch", "schedule": "* * * * * *"},
	}
	for _, p := range bad {
		if _, err := r.ensure(t, wizardv1.StepTrigger, "run-a", "trigger/bad", p); !IsPermanent(err) {
			t.Errorf("%v: want a permanent validation error, got %v", p, err)
		}
	}
	if r.weave.Has("triggers", "t") {
		t.Error("an invalid trigger must not be created")
	}
}

func TestTriggerReuseRequiresIdenticalSettings(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "t", "chain": "ch", "override.ENTRYPOINT": "a.py", "override.MODE": "fast"}
	r.mustDone(t, wizardv1.StepTrigger, "run-a", "trigger", p)

	res := r.mustDone(t, wizardv1.StepTrigger, "run-b", "trigger", p)
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("identical settings must be adopted, got %v", res.Resources[0].Disposition)
	}
	// Spectra silently reuses a trigger by name; the wizard must not.
	for name, change := range map[string]func(map[string]string){
		"other entrypoint": func(m map[string]string) { m["override.ENTRYPOINT"] = "b.py" },
		"other chain":      func(m map[string]string) { m["chain"] = "other" },
		"extra override":   func(m map[string]string) { m["override.EXTRA"] = "1" },
		"cron":             func(m map[string]string) { m["type"], m["schedule"] = "Cron", "0 0 * * * *" },
	} {
		q := map[string]string{}
		for k, v := range p {
			q[k] = v
		}
		change(q)
		if _, err := r.ensure(t, wizardv1.StepTrigger, "run-c", "trigger", q); !IsPermanent(err) {
			t.Errorf("%s: want a conflict, got %v", name, err)
		}
	}
}

// ---- ledger interplay ----

func TestEnsureWaitsWhileAPreviousOwnerIsDeleting(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	p := map[string]string{"name": "ch", "jobTemplate": "tpl"}
	r.mustDone(t, wizardv1.StepChain, "run-a", "chain", p)

	// run-a's rollback dies right after marking the entry Terminating.
	key := ledger.Key{Service: wizardv1.ServiceWeave, Kind: KindChain, Name: "ch"}
	if out, _, err := r.led.Release(ctx, key, wizardv1.ResourceReference{Run: "run-a", Step: "chain"}); err != nil || out != ledger.OutcomeDelete {
		t.Fatalf("release: %v %v", out, err)
	}

	res, err := r.ensure(t, wizardv1.StepChain, "run-b", "chain", p)
	if err != nil || res.Done || res.Requeue != retryTerminating || !strings.Contains(res.Message, "previous owner") {
		t.Fatalf("a terminating resource is a wait, not an error: %+v, %v", res, err)
	}
	if r.log.Count("create weave/chains/") != 1 {
		t.Error("run-b must not create anything while the old resource is being deleted")
	}

	// Anyone's rollback (here: a sweep) completes the interrupted deletion ...
	if err := r.env.SweepTerminating(ctx); err != nil {
		t.Fatal(err)
	}
	if r.weave.Has("chains", "ch") || r.entry(t, wizardv1.ServiceWeave, KindChain, "ch") != nil {
		t.Fatal("the sweep must delete the resource and its ledger entry")
	}
	// ... and then run-b creates a fresh one.
	r.mustDone(t, wizardv1.StepChain, "run-b", "chain", p)
	if r.log.Count("create weave/chains/") != 2 {
		t.Errorf("events: %v", r.log.Snapshot())
	}
}

func TestTransientCreateFailureIsRecoverableAndRollbackSafe(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	p := map[string]string{"name": "ch", "jobTemplate": "tpl"}

	failing := true
	r.weave.CreateHook = func(_ *stepstest.Weave, _ string, _ upstream.WeaveObject) error {
		if failing {
			return stepstest.APIErr(503)
		}
		return nil
	}
	_, err := r.ensure(t, wizardv1.StepChain, "run-a", "chain", p)
	if err == nil || IsPermanent(err) {
		t.Fatalf("a 503 must be a transient error, got %v", err)
	}
	// Write-ahead: the ledger already knows about the resource that was never created.
	e := r.entry(t, wizardv1.ServiceWeave, KindChain, "ch")
	if e == nil || !e.Spec.Managed || len(e.Spec.Refs) != 1 || r.weave.Has("chains", "ch") {
		t.Fatalf("expected a managed entry with no upstream resource, got %+v", e)
	}

	// A rollback at this point must clean the entry up even though run status never saw the step.
	if err := r.env.Rollback(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if r.entry(t, wizardv1.ServiceWeave, KindChain, "ch") != nil {
		t.Fatal("rollback must release references of steps that failed midway")
	}

	// Retrying after the outage works and reports the run as the creator.
	failing = false
	res := r.mustDone(t, wizardv1.StepChain, "run-a", "chain", p)
	if res.Resources[0].Disposition != wizardv1.DispositionCreated || !r.weave.Has("chains", "ch") {
		t.Errorf("retry: %+v", res.Resources)
	}
}

func TestCreateRaceIsResolvedByAdopting(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "ch", "jobTemplate": "tpl"}

	// Between our Get (absent) and our Create, somebody else creates the same chain: the create
	// answers 409. The step must re-check and adopt instead of failing.
	r.weave.CreateHook = func(w *stepstest.Weave, collection string, obj upstream.WeaveObject) error {
		w.Objs[collection+"/ch"] = obj
		return stepstest.APIErr(409)
	}
	res := r.mustDone(t, wizardv1.StepChain, "run-a", "chain", p)
	if len(res.Resources) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if e := r.entry(t, wizardv1.ServiceWeave, KindChain, "ch"); e == nil || len(e.Spec.Refs) != 1 {
		t.Fatalf("entry = %+v", e)
	}
}

func TestRollbackOrderDeletesWatcherBeforeArtifact(t *testing.T) {
	r := newRig(t) // no pre-seeded watchers: the step must create (and so own) the watcher
	r.index.AddArtifact(42, "app.nightly", "1.0.0")
	r.forge.SetBuild(succeeded(1))

	r.mustDone(t, wizardv1.StepGitWatcher, "run-a", "watcher", gwParams("wa", repoURL))
	if _, err := r.wait(t, Input{Run: "run-a", Key: "build", Params: map[string]string{"watcher": "wa"}}); err != nil {
		t.Fatal(err)
	}
	r.mustDone(t, wizardv1.StepTag, "run-a", "tag", tagParams("1.0.0"))
	r.mustDone(t, wizardv1.StepJobTemplate, "run-a", "template", tplParams("app.nightly"))
	r.mustDone(t, wizardv1.StepChain, "run-a", "chain", map[string]string{"name": "ch", "jobTemplate": "tpl"})
	r.mustDone(t, wizardv1.StepTrigger, "run-a", "trigger", map[string]string{"name": "tr", "chain": "ch"})

	if err := r.env.Rollback(context.Background(), "run-a"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"delete weave/triggers/tr",
		"delete weave/chains/ch",
		"delete weave/jobtemplates/tpl",
		"delete forge/gitwatcher/wa", // before the artifact, or forge would rebuild it
		"delete index/tag/42/stable",
		"delete index/artifact/42",
	}
	var got []string
	for _, e := range r.log.Snapshot() {
		if strings.HasPrefix(e, "delete ") {
			got = append(got, e)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rollback order:\n got %v\nwant %v", got, want)
	}
	if entries, _ := r.led.List(context.Background()); len(entries) != 0 {
		t.Errorf("ledger must be empty after a full rollback, has %d entries", len(entries))
	}
	if err := r.env.Rollback(context.Background(), "run-a"); err != nil {
		t.Errorf("a second rollback must be a no-op, got %v", err)
	}
}
