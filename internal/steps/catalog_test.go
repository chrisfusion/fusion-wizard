// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func TestCatalogueIsConsistent(t *testing.T) {
	if len(catalogueOrder) != len(registry) {
		t.Errorf("catalogue lists %d steps but %d are registered", len(catalogueOrder), len(registry))
	}
	for _, typ := range catalogueOrder {
		step, ok := Lookup(typ)
		if !ok || step.Type() != typ {
			t.Errorf("step %s is not registered under its own type", typ)
			continue
		}
		if len(step.Outputs()) == 0 || len(step.Params().Required) == 0 {
			t.Errorf("step %s must declare outputs and required params", typ)
		}
	}
	for _, kind := range []string{KindTrigger, KindChain, KindJobTemplate, KindGitWatcher, KindTag, KindArtifact} {
		if _, ok := undoRank[kind]; !ok {
			t.Errorf("resource kind %q has no undo rank", kind)
		}
	}
	// The ordering rules that make rollback correct.
	if !(undoRank[KindTrigger] < undoRank[KindChain] && undoRank[KindChain] < undoRank[KindJobTemplate]) {
		t.Error("trigger -> chain -> template must be deleted top-down")
	}
	if !(undoRank[KindGitWatcher] < undoRank[KindArtifact] && undoRank[KindGitWatcher] < undoRank[KindTag]) {
		t.Error("the watcher must be deleted before the artifact and tag it feeds, or forge rebuilds them")
	}
	if !(undoRank[KindTag] < undoRank[KindArtifact]) {
		t.Error("the tag must be deleted before its artifact")
	}
	if rankOf("something-new") <= undoRank[KindArtifact] {
		t.Error("unknown kinds must be deleted last")
	}
}

func js(raw string) *apiextensionsv1.JSON { return &apiextensionsv1.JSON{Raw: []byte(raw)} }

// pythonJob is the definition of spectra's "Git Python job" wizard, expressed in the catalogue.
func pythonJob() *wizardv1.WizardDefinitionSpec {
	return &wizardv1.WizardDefinitionSpec{
		Parameters: []wizardv1.WizardParameter{
			{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`},
			{Name: "repoUrl", Required: true},
			{Name: "repoRef", Default: js(`"main"`)},
			{Name: "entrypoints", Type: wizardv1.ParameterStringList, Required: true},
		},
		Steps: []wizardv1.WizardStep{
			{Name: "watcher", Type: wizardv1.StepGitWatcher, Params: map[string]string{"name": "${params.jobName}", "repoUrl": "${params.repoUrl}", "repoRef": "${params.repoRef}"}},
			{Name: "build", Type: wizardv1.StepWaitBuild, Params: map[string]string{"watcher": "${steps.watcher.outputs.name}"}},
			{Name: "tag", Type: wizardv1.StepTag, Params: map[string]string{
				"artifactId": "${steps.build.outputs.artifactId}", "artifactName": "${steps.build.outputs.artifactName}",
				"version": "${steps.build.outputs.version}", "tag": "${config.tagName}"}},
			{Name: "template", Type: wizardv1.StepJobTemplate, Params: map[string]string{
				"name": "${params.jobName}", "artifactName": "${steps.build.outputs.artifactName}", "tag": "${steps.tag.outputs.tag}", "image": "${config.runnerImage}"}},
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.jobName}", "jobTemplate": "${steps.template.outputs.name}"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, ForEach: "${params.entrypoints}", Params: map[string]string{
				"name": "${params.jobName|k8sName}-${item|stem|k8sName}", "chain": "${steps.chain.outputs.name}", "override.ENTRYPOINT": "${item}"}},
		},
	}
}

func TestValidateDefinitionAcceptsThePythonJobWizard(t *testing.T) {
	if err := ValidateDefinition(pythonJob()); err != nil {
		t.Fatalf("the reference definition must be valid: %v", err)
	}
	// Any subset in catalogue order is fine: not every wizard needs every step.
	subset := &wizardv1.WizardDefinitionSpec{
		Parameters: []wizardv1.WizardParameter{{Name: "n", Required: true}},
		Steps: []wizardv1.WizardStep{
			{Name: "chain", Type: wizardv1.StepChain, Params: map[string]string{"name": "${params.n}", "jobTemplate": "existing"}},
			{Name: "trigger", Type: wizardv1.StepTrigger, Params: map[string]string{"name": "${params.n}", "chain": "${steps.chain.outputs.name}"}},
		},
	}
	if err := ValidateDefinition(subset); err != nil {
		t.Errorf("a subset must validate: %v", err)
	}
}

func TestValidateDefinitionRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(d *wizardv1.WizardDefinitionSpec)
		wantSub string
	}{
		{"unknown step type", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Type = "teleport" }, "unknown type"},
		{"out of order", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0], d.Steps[1] = d.Steps[1], d.Steps[0] }, "out of order"},
		{"missing required param", func(d *wizardv1.WizardDefinitionSpec) { delete(d.Steps[0].Params, "repoUrl") }, `missing required param "repoUrl"`},
		{"unknown param (typo)", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoUrll"] = "x" }, `unknown param "repoUrll"`},
		{"undeclared parameter", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["name"] = "${params.nope}" }, `undeclared parameter "nope"`},
		{"unknown config key", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[2].Params["tag"] = "${config.tagname}" }, `unknown config key "tagname"`},
		{"reference to a later step", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoRef"] = "${steps.chain.outputs.name}" }, "does not run before it"},
		{"unknown output", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[1].Params["watcher"] = "${steps.watcher.outputs.id}" }, `output "id"`},
		{"item without forEach", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoRef"] = "${item}" }, "no forEach"},
		{"forEach on a scalar", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[5].ForEach = "${params.jobName}" }, "must be of type stringList"},
		{"forEach undeclared", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[5].ForEach = "${params.zzz}" }, "undeclared parameter"},
		{"forEach with text around", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[5].ForEach = "x${params.entrypoints}" }, "single ${params."},
		{"list used as string", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoRef"] = "${params.entrypoints}" }, "only be used as forEach"},
		{"bad placeholder", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoRef"] = "${params.x" }, "unterminated"},
		{"unknown filter", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[0].Params["repoRef"] = "${params.repoRef|shout}" }, "unknown filter"},
		{"duplicate parameter", func(d *wizardv1.WizardDefinitionSpec) { d.Parameters = append(d.Parameters, d.Parameters[0]) }, "declared twice"},
		{"duplicate step name", func(d *wizardv1.WizardDefinitionSpec) { d.Steps[4].Name = "template" }, "used twice"},
		{"invalid pattern", func(d *wizardv1.WizardDefinitionSpec) { d.Parameters[0].Pattern = "(" }, "invalid pattern"},
		{"reference to a forEach step's output", func(d *wizardv1.WizardDefinitionSpec) {
			d.Steps = append(d.Steps, wizardv1.WizardStep{Name: "after", Type: wizardv1.StepTrigger, Params: map[string]string{
				"name": "x", "chain": "${steps.trigger.outputs.name}"}})
		}, "outputs are ambiguous"},
		{"default of the wrong type", func(d *wizardv1.WizardDefinitionSpec) { d.Parameters[2].Default = js(`5`) }, "invalid parameter default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := pythonJob()
			c.mutate(d)
			err := ValidateDefinition(d)
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("want error containing %q, got %v", c.wantSub, err)
			}
		})
	}
}

func TestValidateDefinitionReportsEverything(t *testing.T) {
	d := pythonJob()
	d.Steps[0].Params["repoUrll"] = "x"
	delete(d.Steps[4].Params, "name")
	d.Steps[2].Params["tag"] = "${config.nope}"
	err := ValidateDefinition(d)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, sub := range []string{`"repoUrll"`, `missing required param "name"`, `"nope"`} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error should mention %s:\n%v", sub, err)
		}
	}
}
