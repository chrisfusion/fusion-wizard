// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package instancecfg

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func minimal() map[string]string {
	return map[string]string{
		KeyRunnerImage: "registry.example/runner:1.0.0",
		KeyForgeURL:    "http://forge.ns.svc:8080/",
		KeyIndexURL:    "http://index.ns.svc:8080",
		KeyWeaveURL:    "https://weave.ns.svc:8082",
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(minimal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagName != "stable" || cfg.ArtifactPrefix != "app." || cfg.ChainStepName != "run" {
		t.Errorf("string defaults wrong: %+v", cfg)
	}
	if cfg.PollInterval != 5*time.Second || cfg.BuildTimeout != 10*time.Minute {
		t.Errorf("duration defaults wrong: %v %v", cfg.PollInterval, cfg.BuildTimeout)
	}
	if cfg.ForgeURL != "http://forge.ns.svc:8080" {
		t.Errorf("trailing slash must be trimmed, got %q", cfg.ForgeURL)
	}
	if got := cfg.DefaultResources.Limits.Memory().String(); got != "1Gi" {
		t.Errorf("default memory limit = %s", got)
	}
}

func TestParseOverrides(t *testing.T) {
	data := minimal()
	data[KeyTagName] = "beta"
	data[KeyArtifactPrefix] = "venv."
	data[KeyChainStepName] = "main"
	data[KeyPollInterval] = "2s"
	data[KeyBuildTimeout] = "30m"
	data[KeyDefaultResources] = "requests:\n  cpu: 100m\nlimits:\n  memory: 512Mi\n"
	cfg, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagName != "beta" || cfg.ArtifactPrefix != "venv." || cfg.ChainStepName != "main" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.PollInterval != 2*time.Second || cfg.BuildTimeout != 30*time.Minute {
		t.Errorf("durations not applied: %v %v", cfg.PollInterval, cfg.BuildTimeout)
	}
	if cfg.DefaultResources.Requests.Cpu().String() != "100m" || cfg.DefaultResources.Limits.Memory().String() != "512Mi" {
		t.Errorf("resources not applied: %+v", cfg.DefaultResources)
	}
	if _, ok := cfg.DefaultResources.Limits[corev1.ResourceCPU]; ok {
		t.Error("an explicit defaultResources value replaces the defaults, it must not merge with them")
	}
}

func TestParseReportsAllProblems(t *testing.T) {
	_, err := Parse(map[string]string{
		KeyForgeURL:         "ftp://forge",
		KeyIndexURL:         "not a url",
		KeyPollInterval:     "soon",
		KeyBuildTimeout:     "-5m",
		KeyDefaultResources: "limits: [1, 2]",
		"pollIntervall":     "5s",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, sub := range []string{
		"unknown key(s): pollIntervall",
		"runnerImage is required",
		"forgeURL",
		"indexURL",
		"weaveURL is required",
		"pollInterval",
		"buildTimeout",
		"defaultResources",
	} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error missing %q:\n%v", sub, err)
		}
	}
}

func TestParseRejectsUnknownResourceFields(t *testing.T) {
	data := minimal()
	data[KeyDefaultResources] = "limitz:\n  cpu: 1\n"
	if _, err := Parse(data); err == nil {
		t.Fatal("a misspelled resources field must be rejected, not ignored")
	}
}

func TestValues(t *testing.T) {
	cfg, err := Parse(minimal())
	if err != nil {
		t.Fatal(err)
	}
	v := cfg.Values()
	if v[KeyTagName] != "stable" || v[KeyRunnerImage] != "registry.example/runner:1.0.0" || v[KeyPollInterval] != "5s" {
		t.Errorf("Values() = %v", v)
	}
	if _, ok := v[KeyDefaultResources]; ok {
		t.Error("structured defaultResources must not be exposed as a string placeholder")
	}
}

func TestLoad(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fusion", Name: "wizard-config"},
		Data:       minimal(),
	}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(cm).Build()

	cfg, err := Load(context.Background(), c, "fusion", "wizard-config")
	if err != nil || cfg.RunnerImage != "registry.example/runner:1.0.0" {
		t.Fatalf("Load: %+v, %v", cfg, err)
	}

	_, err = Load(context.Background(), c, "fusion", "missing")
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("a missing ConfigMap must surface NotFound, got %v", err)
	}
}
