// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"strings"
	"testing"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func imageServiceApp() map[string]string {
	return map[string]string{"name": "cust-a", "type": "service", "image": "registry.example/a/app:1.4.2", "port": "8080", "ingressName": "cust-a"}
}

func appSpec(r *rig, name string) map[string]any {
	return r.weave.Objs["apps/"+name]["spec"].(map[string]any)
}

func TestAppServiceImageOnly(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepApp, "run-a", "app", imageServiceApp())

	obj := r.weave.Objs["apps/cust-a"]
	if obj["kind"] != "WeaveApp" {
		t.Errorf("kind = %v", obj["kind"])
	}
	spec := appSpec(r, "cust-a")
	if spec["type"] != "service" || spec["image"] != "registry.example/a/app:1.4.2" {
		t.Errorf("spec = %v", spec)
	}
	if _, has := spec["codeSource"]; has {
		t.Errorf("an image app must have no codeSource: %v", spec)
	}
	svc := spec["service"].(map[string]any)
	if svc["ingressName"] != "cust-a" || svc["ports"].([]any)[0].(map[string]any)["port"] != 8080 {
		t.Errorf("service = %v", svc)
	}
	labels := obj["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels[ledger.LabelManagedBy] != ledger.ManagedByWizard || labels[ledger.LabelRun] != "run-a" {
		t.Errorf("owner labels = %v", labels)
	}
	if _, has := obj["metadata"].(map[string]any)["annotations"]; has {
		t.Errorf("a service must carry no fire annotation: %v", obj["metadata"])
	}
}

func TestAppCodeSourceUsesTheInstanceRunnerImage(t *testing.T) {
	r := newRig(t)
	p := map[string]string{"name": "svc", "type": "service", "artifactName": "app.svc", "tag": "stable", "port": "8501"}
	r.mustDone(t, wizardv1.StepApp, "run-a", "app", p)

	spec := appSpec(r, "svc")
	cs := spec["codeSource"].(map[string]any)
	if spec["image"] != "registry.example/runner:1.0.0" || cs["artifactName"] != "app.svc" || cs["tag"] != "stable" {
		t.Errorf("spec = %v", spec)
	}
	if _, has := spec["service"].(map[string]any)["ingressName"]; has {
		t.Errorf("no ingressName was given: %v", spec["service"])
	}

	// The runner image is an instance default and not part of the identity: a changed default
	// must not turn the existing app into a conflict.
	r.env.Cfg.RunnerImage = "registry.example/runner:2.0.0"
	if res := r.mustDone(t, wizardv1.StepApp, "run-b", "app", p); res.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("run-b must adopt: %+v", res.Resources)
	}
}

func TestAppIsASharedLedgerResource(t *testing.T) {
	r := newRig(t)
	r.mustDone(t, wizardv1.StepApp, "run-a", "app", imageServiceApp())
	res := r.mustDone(t, wizardv1.StepApp, "run-b", "app", imageServiceApp())
	if res.Resources[0].Disposition != wizardv1.DispositionAdopted || r.log.Count("create weave/apps/") != 1 {
		t.Errorf("run-b must adopt: %+v (events %v)", res.Resources, r.log.Snapshot())
	}

	other := imageServiceApp()
	other["image"] = "registry.example/b/app:2.0.0"
	if _, err := r.ensure(t, wizardv1.StepApp, "run-c", "app", other); !IsPermanent(err) {
		t.Errorf("a different image under the same name must conflict, got %v", err)
	}

	if err := r.env.Rollback(t.Context(), "run-a"); err != nil || !r.weave.Has("apps", "cust-a") {
		t.Fatalf("the app is still referenced by run-b: err=%v events=%v", err, r.log.Snapshot())
	}
	if err := r.env.Rollback(t.Context(), "run-b"); err != nil || r.weave.Has("apps", "cust-a") {
		t.Errorf("the last reference must delete the app: err=%v events=%v", err, r.log.Snapshot())
	}
}

func TestAppJobEntrypoints(t *testing.T) {
	r := newRig(t)
	p := map[string]string{
		"name": "nightly", "type": "job", "artifactName": "app.nightly", "tag": "stable",
		"entrypoints":      `[{"key":"main.py","type":"OnDemand","schedule":""},{"key":"etl/load.py","type":"Cron","schedule":"0 0 9 * * *"}]`,
		"externalAuthMode": "serviceAccount", "externalAuthName": "sa",
	}
	r.mustDone(t, wizardv1.StepApp, "run-a", "app", p)

	spec := appSpec(r, "nightly")
	eps := spec["entrypoints"].([]any)
	if len(eps) != 2 {
		t.Fatalf("entrypoints = %v", eps)
	}
	onDemand, cron := eps[0].(map[string]any), eps[1].(map[string]any)
	if onDemand["name"] != "main" || cron["name"] != "etl-load" || cron["schedule"] != "0 0 9 * * *" {
		t.Errorf("entrypoints = %v", eps)
	}
	if _, has := onDemand["schedule"]; has {
		t.Error("an OnDemand entrypoint must not carry a schedule")
	}
	env := cron["env"].([]any)[0].(map[string]any)
	if env["name"] != "ENTRYPOINT" || env["value"] != "etl/load.py" {
		t.Errorf("env = %v", env)
	}
	if auth := spec["externalAuthRef"].(map[string]any); auth["mode"] != "serviceAccount" || auth["name"] != "sa" {
		t.Errorf("externalAuthRef = %v", auth)
	}
	if _, has := r.weave.Objs["apps/nightly"]["metadata"].(map[string]any)["annotations"]; has {
		t.Error("nothing may be fired for an entrypoints list")
	}

	// Another schedule under the same name is a conflict, the same one a re-confirm.
	p2 := map[string]string{}
	for k, v := range p {
		p2[k] = v
	}
	p2["entrypoints"] = strings.Replace(p["entrypoints"], "0 0 9", "0 0 10", 1)
	if _, err := r.ensure(t, wizardv1.StepApp, "run-b", "app", p2); !IsPermanent(err) {
		t.Errorf("a different schedule must conflict, got %v", err)
	}
	if res := r.mustDone(t, wizardv1.StepApp, "run-c", "app", p); res.Resources[0].Disposition != wizardv1.DispositionAdopted {
		t.Errorf("identical settings must adopt: %+v", res.Resources)
	}
}

func TestAppSingleJobEntrypointAndFireOnCreate(t *testing.T) {
	r := newRig(t)
	once := map[string]string{"name": "once", "type": "job", "image": "registry.example/a/job:1", "fireOnCreate": "true"}
	r.mustDone(t, wizardv1.StepApp, "run-a", "app", once)

	eps := appSpec(r, "once")["entrypoints"].([]any)
	if len(eps) != 1 || eps[0].(map[string]any)["name"] != "main" {
		t.Fatalf("entrypoints = %v", eps)
	}
	if got := r.weave.Objs["apps/once"]["metadata"].(map[string]any)["annotations"].(map[string]any)["fusion-platform.io/fire"]; got != "main" {
		t.Errorf("fire annotation = %v, want main", got)
	}
	// Adopting must not fire again: nothing is created, so no annotation is sent.
	r.mustDone(t, wizardv1.StepApp, "run-b", "app", once)
	if r.log.Count("create weave/apps/") != 1 {
		t.Errorf("an adopt must not create: %v", r.log.Snapshot())
	}

	cron := map[string]string{"name": "nightly", "type": "job", "image": "registry.example/a/job:1", "schedule": "0 0 9 * * *"}
	r.mustDone(t, wizardv1.StepApp, "run-c", "app", cron)
	ep := appSpec(r, "nightly")["entrypoints"].([]any)[0].(map[string]any)
	if ep["schedule"] != "0 0 9 * * *" {
		t.Errorf("entrypoint = %v", ep)
	}
	if _, has := r.weave.Objs["apps/nightly"]["metadata"].(map[string]any)["annotations"]; has {
		t.Error("a cron job must not be fired on creation")
	}
}

func TestValidateAppParams(t *testing.T) {
	mut := func(base map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	svc := imageServiceApp()
	job := map[string]string{"name": "j", "type": "job", "image": "registry.example/a/job:1"}
	eps := `[{"key":"a.py","type":"OnDemand","schedule":""}]`

	bad := map[string]struct {
		params map[string]string
		want   string
	}{
		"unknown type":          {mut(svc, "type", "daemon"), "not supported"},
		"latest image":          {mut(svc, "image", "registry.example/a/app:latest"), "explicit tag"},
		"untagged image":        {mut(svc, "image", "registry.example/a/app"), "explicit tag"},
		"no image":              {mut(svc, "image", ""), "empty"},
		"artifact without tag":  {mut(svc, "artifactName", "app.x"), "together"},
		"bad port":              {mut(svc, "port", "http"), "valid port"},
		"service with schedule": {mut(svc, "schedule", "0 0 9 * * *"), "only valid for a job"},
		"service with auth":     {mut(svc, "externalAuthMode", "oidc", "externalAuthName", "kc"), "service app"},
		"half-set auth":         {mut(job, "externalAuthMode", "oidc"), "must be set together"},
		"job with port":         {mut(job, "port", "80"), "only valid for a service"},
		"entrypoints+schedule":  {mut(job, "entrypoints", eps, "schedule", "0 0 9 * * *"), "without an entrypoints list"},
		"entrypoints+fire":      {mut(job, "entrypoints", eps, "fireOnCreate", "true"), "without an entrypoints list"},
		"empty entrypoints":     {mut(job, "entrypoints", `[]`), "must not be empty"},
		"entrypoints not json":  {mut(job, "entrypoints", `main.py`), "JSON"},
		"cron without schedule": {mut(job, "entrypoints", `[{"key":"a.py","type":"Cron","schedule":""}]`), "needs a schedule"},
		"schedule on demand":    {mut(job, "entrypoints", `[{"key":"a.py","type":"OnDemand","schedule":"0 0 9 * * *"}]`), "only valid for a Cron"},
		"unknown entry type":    {mut(job, "entrypoints", `[{"key":"a.py","type":"Weekly","schedule":""}]`), "not supported"},
		"colliding names":       {mut(job, "entrypoints", `[{"key":"a.py","type":""},{"key":"a.sh","type":""}]`), "both map to the name"},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			err := ValidateAppParams(wizardv1.StepApp, c.params)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}

	for name, p := range map[string]map[string]string{
		"service":            svc,
		"codeSource service": {"name": "s", "type": "service", "artifactName": "app.s", "tag": "stable"},
		"single job":         mut(job, "fireOnCreate", "true"),
		"cron job":           mut(job, "schedule", "0 0 9 * * *"),
		"entrypoints":        mut(job, "entrypoints", eps, "externalAuthMode", "oidc", "externalAuthName", "kc"),
	} {
		if err := ValidateAppParams(wizardv1.StepApp, p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Values that are not known yet are skipped, everything else is still checked.
	pending := mut(job, "artifactName", Pending, "tag", Pending, "image", Pending)
	if err := ValidateAppParams(wizardv1.StepApp, pending); err != nil {
		t.Errorf("pending values must not fail: %v", err)
	}
	if err := ValidateAppParams(wizardv1.StepApp, mut(pending, "externalAuthMode", "basic", "externalAuthName", "x")); err == nil {
		t.Error("a bad auth mode must still be caught while the image is pending")
	}
	if err := ValidateAppParams(wizardv1.StepTag, map[string]string{"type": "x"}); err != nil {
		t.Errorf("other step types pass: %v", err)
	}
}
