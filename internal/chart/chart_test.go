// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package chart tests the Helm chart's contract with the code: what the chart renders must be what
// the binaries accept. It shells out to `helm template`, so it is skipped where helm is missing.
package chart

import (
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/yaml"

	"fusion-platform.io/fusion-wizard/internal/apiserver"
	"fusion-platform.io/fusion-wizard/internal/envutil"
	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/steps"
	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	chartDir  = "../../deployment/fusion-wizard"
	namespace = "team-a"
)

var goodArgs = []string{"--set", "config.runnerImage=fusion-runner:1.0.0", "--set", "api.auth.allowedServiceAccounts={fusion/fusion-bff}"}

type object map[string]any

func (o object) kind() string { return str(o, "kind") }
func (o object) name() string { return str(nestedMap(o, "metadata"), "name") }

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func nestedMap(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

// render runs helm template and returns the parsed objects, or the helm error output.
func render(t *testing.T, extra ...string) ([]object, string, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	args := append([]string{"template", "rel", chartDir, "-n", namespace}, extra...)
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}
	var objs []object
	for _, doc := range strings.Split(string(out), "\n---\n") {
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("helm output is not valid YAML: %v\n%s", err, doc)
		}
		if o.kind() != "" {
			objs = append(objs, o)
		}
	}
	return objs, string(out), nil
}

func mustRender(t *testing.T, extra ...string) []object {
	t.Helper()
	objs, out, err := render(t, extra...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return objs
}

func find(t *testing.T, objs []object, kind, name string) object {
	t.Helper()
	for _, o := range objs {
		if o.kind() == kind && o.name() == name {
			return o
		}
	}
	t.Fatalf("%s %q not rendered", kind, name)
	return nil
}

// reroundtrip converts a generic object into a typed one.
func reroundtrip[T any](t *testing.T, o object) T {
	t.Helper()
	raw, err := yaml.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := yaml.UnmarshalStrict(raw, &v); err != nil {
		t.Fatalf("object does not fit its Go type: %v\n%s", err, raw)
	}
	return v
}

func TestPreseededDefinitionIsValidAndMatchesTheCode(t *testing.T) {
	def := reroundtrip[wizardv1.WizardDefinition](t, find(t, mustRender(t, goodArgs...), "WizardDefinition", "python-git-job"))

	if err := steps.ValidateDefinition(&def.Spec); err != nil {
		t.Fatalf("the shipped definition is invalid: %v", err)
	}
	// The chart's YAML and the test fixture describe the same wizard; drift means one of them is stale.
	if want := stepstest.PythonJob(); !equality.Semantic.DeepEqual(def.Spec, *want) {
		got, _ := yaml.Marshal(def.Spec)
		exp, _ := yaml.Marshal(want)
		t.Errorf("chart definition differs from stepstest.PythonJob\n--- chart ---\n%s\n--- code ---\n%s", got, exp)
	}
	if def.Namespace != namespace {
		t.Errorf("definition namespace = %q", def.Namespace)
	}

	objs := mustRender(t, append([]string{"--set", "definitions.pythonGitJob.enabled=false"}, goodArgs...)...)
	for _, o := range objs {
		if o.kind() == "WizardDefinition" && o.name() == "python-git-job" {
			t.Error("the definition must be switchable off")
		}
	}
}

func TestBatchJobDefinitionIsValidAndMatchesTheCode(t *testing.T) {
	def := reroundtrip[wizardv1.WizardDefinition](t, find(t, mustRender(t, goodArgs...), "WizardDefinition", "batch-git-job"))

	if err := steps.ValidateDefinition(&def.Spec); err != nil {
		t.Fatalf("the shipped definition is invalid: %v", err)
	}
	if want := stepstest.BatchJob(); !equality.Semantic.DeepEqual(def.Spec, *want) {
		got, _ := yaml.Marshal(def.Spec)
		exp, _ := yaml.Marshal(want)
		t.Errorf("chart definition differs from stepstest.BatchJob\n--- chart ---\n%s\n--- code ---\n%s", got, exp)
	}
	if def.Namespace != namespace {
		t.Errorf("definition namespace = %q", def.Namespace)
	}

	objs := mustRender(t, append([]string{"--set", "definitions.batchGitJob.enabled=false"}, goodArgs...)...)
	for _, o := range objs {
		if o.kind() == "WizardDefinition" && o.name() == "batch-git-job" {
			t.Error("the definition must be switchable off")
		}
	}
}

func TestBatchCronJobDefinitionIsValidAndMatchesTheCode(t *testing.T) {
	def := reroundtrip[wizardv1.WizardDefinition](t, find(t, mustRender(t, goodArgs...), "WizardDefinition", "batchcron-git-job"))

	if err := steps.ValidateDefinition(&def.Spec); err != nil {
		t.Fatalf("the shipped definition is invalid: %v", err)
	}
	if want := stepstest.BatchCronJob(); !equality.Semantic.DeepEqual(def.Spec, *want) {
		got, _ := yaml.Marshal(def.Spec)
		exp, _ := yaml.Marshal(want)
		t.Errorf("chart definition differs from stepstest.BatchCronJob\n--- chart ---\n%s\n--- code ---\n%s", got, exp)
	}
	if def.Namespace != namespace {
		t.Errorf("definition namespace = %q", def.Namespace)
	}

	objs := mustRender(t, append([]string{"--set", "definitions.batchCronGitJob.enabled=false"}, goodArgs...)...)
	for _, o := range objs {
		if o.kind() == "WizardDefinition" && o.name() == "batchcron-git-job" {
			t.Error("the definition must be switchable off")
		}
	}
}

func TestInstanceConfigParsesWithTheRealLoader(t *testing.T) {
	cm := find(t, mustRender(t, goodArgs...), "ConfigMap", "rel-config")
	data := map[string]string{}
	for k, v := range nestedMap(cm, "data") {
		data[k], _ = v.(string)
	}
	cfg, err := instancecfg.Parse(data) // rejects unknown keys, so a renamed key breaks this test
	if err != nil {
		t.Fatalf("the rendered ConfigMap is not valid instance config: %v", err)
	}
	if cfg.RunnerImage != "fusion-runner:1.0.0" || cfg.TagName != "stable" || cfg.ArtifactPrefix != "app." {
		t.Errorf("config = %+v", cfg)
	}
	// Upstream URLs default to the usual in-cluster services in the RELEASE namespace.
	for got, want := range map[string]string{
		cfg.ForgeURL: "http://fusion-forge.team-a.svc.cluster.local:8080",
		cfg.IndexURL: "http://fusion-index-backend.team-a.svc.cluster.local:8080",
		cfg.WeaveURL: "http://fusion-weave-api.team-a.svc.cluster.local:8082",
	} {
		if got != want {
			t.Errorf("upstream URL = %q, want %q", got, want)
		}
	}
	// The operator must be told the same ConfigMap name.
	op := find(t, mustRender(t, goodArgs...), "Deployment", "rel-operator")
	if args := containerArgs(op); !contains(args, "--config-map=rel-config") {
		t.Errorf("operator args = %v", args)
	}
}

func TestInstanceConfigOverrides(t *testing.T) {
	objs := mustRender(t, append(goodArgs,
		"--set", "upstreams.forge.url=http://forge.other:9000",
		"--set", "config.tagName=beta",
		"--set", "config.defaultResources.limits.memory=2Gi")...)
	cm := find(t, objs, "ConfigMap", "rel-config")
	data := map[string]string{}
	for k, v := range nestedMap(cm, "data") {
		data[k], _ = v.(string)
	}
	cfg, err := instancecfg.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForgeURL != "http://forge.other:9000" || cfg.TagName != "beta" || cfg.DefaultResources.Limits.Memory().String() != "2Gi" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestChartFailsEarlyOnMissingRequiredValues(t *testing.T) {
	_, out, err := render(t, "--set", "api.auth.allowedServiceAccounts={fusion/fusion-bff}")
	if err == nil || !strings.Contains(out, "config.runnerImage is required") {
		t.Errorf("a missing runnerImage must fail the render with a clear message: %v\n%s", err, out)
	}
	_, out, err = render(t, "--set", "config.runnerImage=x")
	if err == nil || !strings.Contains(out, "api.auth.allowedServiceAccounts is required") {
		t.Errorf("an API without named callers must fail the render: %v\n%s", err, out)
	}
	if _, out, err := render(t, "--set", "config.runnerImage=x", "--set", "api.auth.allowUnauthenticated=true"); err != nil {
		t.Errorf("explicit dev mode must render: %v\n%s", err, out)
	}
	if _, out, err := render(t, "--set", "config.runnerImage=x", "--set", "api.enabled=false"); err != nil {
		t.Errorf("the API can be disabled entirely: %v\n%s", err, out)
	}
	_, out, err = render(t, append(goodArgs, "--set", "api.ingress.enabled=true")...)
	if err == nil || !strings.Contains(out, "api.ingress.host is required") {
		t.Errorf("an ingress without a host must fail: %v", err)
	}
}

func TestAPIConfigurationPassesTheServersOwnValidation(t *testing.T) {
	api := find(t, mustRender(t, goodArgs...), "Deployment", "rel-api")
	env := containerEnv(api)
	cfg := apiserver.Config{
		Namespace:              env["NAMESPACE"],
		AllowedServiceAccounts: envutil.Split(env["AUTH_ALLOWED_SA"]),
		AllowUnauthenticated:   env["ALLOW_UNAUTHENTICATED"] == "true",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the rendered API environment is rejected by the server: %v (env %v)", err, env)
	}
	if env["NAMESPACE"] != namespace || env["AUTH_ALLOWED_SA"] != "fusion/fusion-bff" {
		t.Errorf("env = %v", env)
	}
	if got := containerCommand(api); len(got) != 1 || got[0] != "/api-server" {
		t.Errorf("the API deployment must override the entrypoint, command = %v", got)
	}
	if got := containerCommand(find(t, mustRender(t, goodArgs...), "Deployment", "rel-operator")); len(got) != 1 || got[0] != "/manager" {
		t.Errorf("operator command = %v", got)
	}
}

func TestEverythingLivesInTheReleaseNamespace(t *testing.T) {
	for _, o := range mustRender(t, goodArgs...) {
		switch o.kind() {
		case "ClusterRole", "ClusterRoleBinding":
			continue
		}
		if ns := str(nestedMap(o, "metadata"), "namespace"); ns != namespace {
			t.Errorf("%s %s is in namespace %q, want %q (a hardcoded namespace breaks multi-instance deploys)", o.kind(), o.name(), ns, namespace)
		}
	}
	objs := mustRender(t, goodArgs...)
	crb := find(t, objs, "ClusterRoleBinding", "team-a-rel-api-tokenreview")
	subject := crb["subjects"].([]any)[0].(map[string]any)
	if subject["namespace"] != namespace || subject["name"] != "rel-api" {
		t.Errorf("the cluster-scoped binding must name the release's API service account: %v", subject)
	}
	// The chart label must be valid for a label value even with Flux's build metadata in the version.
	for _, o := range objs {
		if v := str(nestedMap(nestedMap(o, "metadata"), "labels"), "helm.sh/chart"); strings.ContainsAny(v, "+") {
			t.Errorf("%s %s: helm.sh/chart label %q contains '+'", o.kind(), o.name(), v)
		}
	}
}

func TestRBACCoversWhatTheCodeNeedsAndNoMore(t *testing.T) {
	objs := mustRender(t, goodArgs...)

	sa := find(t, objs, "ServiceAccount", "rel-operator")
	if got := str(nestedMap(nestedMap(sa, "metadata"), "labels"), "fusion-platform.io/role"); got != "admin" {
		t.Errorf("weave allows DELETE only for admins and rollback deletes weave resources; role label = %q", got)
	}

	op := rules(find(t, objs, "Role", "rel-operator"))
	for _, want := range []string{
		"wizardruns:patch", "wizardruns/status:patch", "wizardruns/finalizers:update", "wizarddefinitions:get",
		"wizardresources:create", "wizardresources:update", "wizardresources:delete", "wizardresources:list",
		"configmaps:get", "leases:create",
	} {
		if !op[want] {
			t.Errorf("operator Role lacks %s", want)
		}
	}
	if op["configmaps:list"] || op["configmaps:watch"] || op["secrets:get"] || op["wizarddefinitions:delete"] {
		t.Error("the operator Role grants more than it needs")
	}

	api := rules(find(t, objs, "Role", "rel-api"))
	for _, want := range []string{"wizardruns:create", "wizardruns:patch", "wizardruns:delete", "wizardruns:list", "wizarddefinitions:list", "wizardresources:list"} {
		if !api[want] {
			t.Errorf("API Role lacks %s", want)
		}
	}
	for _, bad := range []string{"wizardresources:create", "wizardresources:delete", "wizardruns/status:patch", "wizarddefinitions:create", "configmaps:get"} {
		if api[bad] {
			t.Errorf("the API Role must not grant %s: it never provisions", bad)
		}
	}
	tr := rules(find(t, objs, "ClusterRole", "team-a-rel-api-tokenreview"))
	if !tr["tokenreviews:create"] || len(tr) != 1 {
		t.Errorf("the cluster role must only allow creating token reviews: %v", tr)
	}
}

func TestUpstreamTokensAreProjectedPerService(t *testing.T) {
	op := find(t, mustRender(t, append(goodArgs, "--set", "upstreams.forge.auth.audience=fusion-forge", "--set", "upstreams.index.auth.enabled=false")...), "Deployment", "rel-operator")
	env := containerEnv(op)
	if env["FORGE_TOKEN_PATH"] != "/var/run/secrets/wizard/forge/token" || env["WEAVE_TOKEN_PATH"] != "/var/run/secrets/wizard/weave/token" {
		t.Errorf("token paths = %v", env)
	}
	if _, present := env["INDEX_TOKEN_PATH"]; present {
		t.Error("a disabled upstream must not get a token path (the operator treats an empty path as 'no auth')")
	}
	raw, _ := yaml.Marshal(op)
	if !strings.Contains(string(raw), "audience: fusion-forge") || strings.Count(string(raw), "audience:") != 1 {
		t.Errorf("only the configured audience may be set:\n%s", raw)
	}
}

// ---- helpers over the generic deployment object ----

func firstContainer(o object) map[string]any {
	spec := nestedMap(nestedMap(nestedMap(o, "spec"), "template"), "spec")
	return spec["containers"].([]any)[0].(map[string]any)
}

func containerArgs(o object) []string {
	var out []string
	for _, a := range firstContainer(o)["args"].([]any) {
		out = append(out, a.(string))
	}
	return out
}

func containerCommand(o object) []string {
	var out []string
	for _, a := range firstContainer(o)["command"].([]any) {
		out = append(out, a.(string))
	}
	return out
}

func containerEnv(o object) map[string]string {
	out := map[string]string{}
	envList, _ := firstContainer(o)["env"].([]any)
	for _, e := range envList {
		m := e.(map[string]any)
		out[m["name"].(string)], _ = m["value"].(string)
	}
	return out
}

// rules flattens a Role's rules into "resource:verb" entries.
func rules(o object) map[string]bool {
	out := map[string]bool{}
	for _, r := range o["rules"].([]any) {
		rule := r.(map[string]any)
		for _, res := range rule["resources"].([]any) {
			for _, verb := range rule["verbs"].([]any) {
				out[res.(string)+":"+verb.(string)] = true
			}
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
