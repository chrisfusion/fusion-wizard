// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package plan

import (
	"strings"
	"testing"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// definition has five plain steps and one forEach step over the entrypoints parameter.
func definition() wizardv1.WizardDefinitionSpec {
	plain := func(name string, t wizardv1.StepType) wizardv1.WizardStep {
		return wizardv1.WizardStep{Name: name, Type: t}
	}
	return wizardv1.WizardDefinitionSpec{Steps: []wizardv1.WizardStep{
		plain("watcher", wizardv1.StepGitWatcher), plain("build", wizardv1.StepWaitBuild), plain("tag", wizardv1.StepTag),
		plain("template", wizardv1.StepJobTemplate), plain("chain", wizardv1.StepChain),
		{Name: "trigger", Type: wizardv1.StepTrigger, ForEach: "${params.entrypoints}"},
	}}
}

func TestExpand(t *testing.T) {
	def := definition()
	insts, err := Expand(&def, map[string]any{"jobName": "n", "repoUrl": "u", "entrypoints": []string{"a.py", "b/c.py"}})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, in := range insts {
		keys = append(keys, in.Key)
	}
	want := "watcher build tag template chain trigger/a.py trigger/b/c.py"
	if got := strings.Join(keys, " "); got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	if insts[5].Item == nil || *insts[5].Item != "a.py" || insts[0].Item != nil {
		t.Error("only forEach instances carry an item")
	}

	insts, err = Expand(&def, map[string]any{"entrypoints": []string{}})
	if err != nil || len(insts) != 5 {
		t.Errorf("an empty forEach list yields no instances: %d, %v", len(insts), err)
	}
	for _, bad := range [][]string{{"a", "a"}, {""}} {
		if _, err := Expand(&def, map[string]any{"entrypoints": bad}); err == nil {
			t.Errorf("entrypoints %q must be rejected", bad)
		}
	}
	if _, err := Expand(&def, map[string]any{}); err == nil {
		t.Error("a missing list parameter must be an error")
	}
}

func TestExpandObjectList(t *testing.T) {
	def := definition()
	insts, err := Expand(&def, map[string]any{"entrypoints": []map[string]string{
		{"key": "main.py", "type": "OnDemand", "schedule": ""},
		{"key": "report.py", "type": "Cron", "schedule": "0 9 * * *"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 7 {
		t.Fatalf("got %d instances", len(insts))
	}
	last := insts[6]
	if last.Key != "trigger/report.py" || last.Item == nil || *last.Item != "report.py" {
		t.Errorf("last instance = %+v", last)
	}
	if last.ItemFields["type"] != "Cron" || last.ItemFields["schedule"] != "0 9 * * *" {
		t.Errorf("ItemFields = %+v", last.ItemFields)
	}
	if insts[5].ItemFields["key"] != "main.py" {
		t.Errorf("ItemFields = %+v", insts[5].ItemFields)
	}
	if insts[0].ItemFields != nil {
		t.Error("a plain (non-forEach) instance must not carry ItemFields")
	}
}
