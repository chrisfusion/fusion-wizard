// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"testing"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func imageTplParams() map[string]string {
	return map[string]string{"name": "base", "port": "8080"}
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
