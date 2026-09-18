// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package params

import (
	"errors"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

func TestK8sName(t *testing.T) {
	long := strings.Repeat("a", 62) + "-b" // 64 chars, cut at 63 leaves a trailing dash
	cases := map[string]string{
		"My_Job Name":           "my-job-name",
		"--edge--":              "edge",
		"main.py":               "main-py",
		"!!!":                   "x",
		"":                      "x",
		"UPPER":                 "upper",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
		long:                    strings.Repeat("a", 62),
	}
	for in, want := range cases {
		if got := K8sName(in); got != want {
			t.Errorf("K8sName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStem(t *testing.T) {
	cases := map[string]string{
		"main.py":       "main",
		"a.b.py":        "a.b",
		"noext":         "noext",
		".hidden":       ".hidden",
		"dir.v1/run":    "dir.v1/run",
		"dir.v1/run.py": "dir.v1/run",
	}
	for in, want := range cases {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	ctx := Context{
		Params: map[string]any{"jobName": "Nightly Job", "count": float64(3), "debug": true, "files": []string{"a.py"}},
		Config: map[string]string{"tagName": "stable"},
		Steps:  map[string]map[string]string{"build": {"version": "1.2.3"}},
		Item:   strp("Entry_Point.py"),
	}
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"${params.jobName}", "Nightly Job"},
		{"${ params.jobName | k8sName }", "nightly-job"},
		{"${params.count}x${params.debug}", "3xtrue"},
		{"${config.tagName}", "stable"},
		{"v${steps.build.outputs.version}", "v1.2.3"},
		{"${params.jobName|k8sName}-${item|stem|k8sName}", "nightly-job-entry-point"},
		{"$${params.jobName}", "${params.jobName}"},
		{"a$${b}${config.tagName}", "a${b}stable"},
		{"", ""},
	}
	for _, c := range cases {
		got, err := Resolve(c.in, ctx)
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	ctx := Context{
		Params: map[string]any{"files": []string{"a.py"}},
		Config: map[string]string{},
	}
	cases := []struct {
		name, in, wantSub string
		unresolved        bool
	}{
		{"unterminated", "${params.x", "unterminated", false},
		{"bad namespace", "${env.HOME}", "invalid reference", false},
		{"bad steps shape", "${steps.build.version}", "invalid reference", false},
		{"unknown filter", "${params.x|shout}", "unknown filter", false},
		{"missing param", "${params.nope}", "has no value", true},
		{"missing config", "${config.nope}", "no key", true},
		{"missing step", "${steps.build.outputs.v}", "not produced outputs", true},
		{"item outside forEach", "${item}", "forEach", true},
		{"list in string", "${params.files}", "forEach", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Resolve(c.in, ctx)
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("Resolve(%q) error = %v, want substring %q", c.in, err, c.wantSub)
			}
			var ue *UnresolvedError
			if got := errors.As(err, &ue); got != c.unresolved {
				t.Errorf("errors.As(UnresolvedError) = %v, want %v", got, c.unresolved)
			}
		})
	}
}

func TestRefs(t *testing.T) {
	tpl, err := Parse("${params.a}-${steps.s.outputs.k|lower}-$${x}-${item}")
	if err != nil {
		t.Fatal(err)
	}
	refs := tpl.Refs()
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3: %+v", len(refs), refs)
	}
	if refs[0].Kind != RefParams || refs[0].Name != "a" {
		t.Errorf("ref0 = %+v", refs[0])
	}
	if refs[1].Kind != RefSteps || refs[1].Name != "s" || refs[1].Key != "k" || len(refs[1].Filters) != 1 {
		t.Errorf("ref1 = %+v", refs[1])
	}
	if refs[2].Kind != RefItem {
		t.Errorf("ref2 = %+v", refs[2])
	}
}

func TestResolveMap(t *testing.T) {
	ctx := Context{Params: map[string]any{"a": "x"}}
	got, err := ResolveMap(map[string]string{"one": "${params.a}", "two": "lit"}, ctx)
	if err != nil || got["one"] != "x" || got["two"] != "lit" {
		t.Fatalf("got %v, %v", got, err)
	}
	_, err = ResolveMap(map[string]string{"bad1": "${params.no}", "bad2": "${params.nah}"}, ctx)
	if err == nil || !strings.Contains(err.Error(), `"bad1"`) || !strings.Contains(err.Error(), `"bad2"`) {
		t.Fatalf("want both failures reported, got %v", err)
	}
}

func TestResolveList(t *testing.T) {
	ctx := Context{Params: map[string]any{"files": []string{"a.py", "b.py"}, "name": "x"}}
	got, err := ResolveList("${params.files}", ctx)
	if err != nil || len(got) != 2 || got[1] != "b.py" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"${params.name}", "${params.files|lower}", "x${params.files}", "${config.a}", "literal", "${params.nope}"} {
		if _, err := ResolveList(bad, ctx); err == nil {
			t.Errorf("ResolveList(%q): want error", bad)
		}
	}
}
