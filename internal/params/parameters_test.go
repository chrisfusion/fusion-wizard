// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package params

import (
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func js(raw string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(raw)} }
func jsp(raw string) *apiextensionsv1.JSON {
	j := js(raw)
	return &j
}

func TestResolveParametersHappyPath(t *testing.T) {
	defs := []wizardv1.WizardParameter{
		{Name: "jobName", Required: true, Pattern: `^[a-z0-9-]+$`},
		{Name: "repoRef", Default: jsp(`"main"`)},
		{Name: "retries", Type: wizardv1.ParameterNumber, Default: jsp(`2`)},
		{Name: "publish", Type: wizardv1.ParameterBoolean},
		{Name: "entrypoints", Type: wizardv1.ParameterStringList, Required: true, Pattern: `\.py$`},
		{Name: "optional"},
	}
	got, err := ResolveParameters(defs, map[string]apiextensionsv1.JSON{
		"jobName":     js(`"nightly"`),
		"publish":     js(`true`),
		"entrypoints": js(`["a.py","b.py"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"jobName":     "nightly",
		"repoRef":     "main",
		"retries":     float64(2),
		"publish":     true,
		"entrypoints": []string{"a.py", "b.py"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if _, present := got["optional"]; present {
		t.Error("an optional parameter without value or default must stay unset")
	}
}

func TestResolveParametersSuppliedBeatsDefault(t *testing.T) {
	defs := []wizardv1.WizardParameter{{Name: "repoRef", Default: jsp(`"main"`)}}
	got, err := ResolveParameters(defs, map[string]apiextensionsv1.JSON{"repoRef": js(`"release"`)})
	if err != nil || got["repoRef"] != "release" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResolveParametersNullCountsAsUnset(t *testing.T) {
	defs := []wizardv1.WizardParameter{{Name: "repoRef", Default: jsp(`"main"`)}, {Name: "must", Required: true}}
	got, err := ResolveParameters(defs, map[string]apiextensionsv1.JSON{"repoRef": js(`null`), "must": js(`null`)})
	if got["repoRef"] != "main" {
		t.Errorf("null should fall back to the default, got %v", got["repoRef"])
	}
	if err == nil || !strings.Contains(err.Error(), `"must" is required`) {
		t.Errorf("null for a required parameter must fail, got %v", err)
	}
}

func TestResolveParametersReportsEverything(t *testing.T) {
	defs := []wizardv1.WizardParameter{
		{Name: "a", Required: true},
		{Name: "n", Type: wizardv1.ParameterNumber},
		{Name: "b", Type: wizardv1.ParameterBoolean},
		{Name: "l", Type: wizardv1.ParameterStringList, Required: true},
		{Name: "p", Pattern: `^x`},
		{Name: "bad", Pattern: `(`},
		{Name: "s"},
	}
	_, err := ResolveParameters(defs, map[string]apiextensionsv1.JSON{
		"n":     js(`"text"`),
		"b":     js(`1`),
		"l":     js(`[]`),
		"p":     js(`"yes"`),
		"bad":   js(`"v"`),
		"s":     js(`5`),
		"zeta":  js(`1`),
		"alpha": js(`1`),
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, sub := range []string{
		"unknown parameter(s): alpha, zeta",
		`"a" is required`,
		`"n": expected a number`,
		`"b": expected a boolean`,
		`"l": must not be empty`,
		`"p": "yes" does not match pattern ^x`,
		`"bad": definition has an invalid pattern`,
		`"s": expected a string`,
	} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error missing %q:\n%v", sub, err)
		}
	}
}

func TestResolveParametersRequiredEmptyString(t *testing.T) {
	defs := []wizardv1.WizardParameter{{Name: "a", Required: true}}
	if _, err := ResolveParameters(defs, map[string]apiextensionsv1.JSON{"a": js(`""`)}); err == nil {
		t.Fatal("empty string must not satisfy a required parameter")
	}
}
