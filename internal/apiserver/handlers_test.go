// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func entrypointEntries(files ...string) []map[string]any {
	entries := make([]map[string]any, len(files))
	for i, f := range files {
		entries[i] = map[string]any{"key": f, "type": "OnDemand", "schedule": ""}
	}
	return entries
}

func nightly(extra map[string]any) map[string]any {
	p := map[string]any{"jobName": "nightly", "repoUrl": "https://git.example/team/nightly.git", "entrypoints": entrypointEntries("main.py", "etl/load.py")}
	for k, v := range extra {
		p[k] = v
	}
	return map[string]any{"definition": "python-job", "parameters": p}
}

type errResp struct {
	Error   string   `json:"error"`
	Details []string `json:"details"`
}

// ---- create ----

func TestCreateRun(t *testing.T) {
	h := newHarness(t)
	h.seedPythonJob()

	rec := h.doWith(http.MethodPost, "/api/v1/runs", nightly(nil), map[string]string{
		"Authorization": "Bearer " + bffToken, "X-User-ID": "user-42", "X-User-Email": "alice@example.org"})
	expect(t, rec, http.StatusCreated)
	v := decode[runView](t, rec)

	if !strings.HasPrefix(v.Name, "python-job-") || len(v.Name) <= len("python-job-") {
		t.Errorf("a generated name must extend the definition name, got %q", v.Name)
	}
	if rec.Header().Get("Location") != "/api/v1/runs/"+v.Name {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	if v.Definition != "python-job" || v.DesiredState != wizardv1.DesiredApplied || v.Status.Phase != wizardv1.RunPending ||
		v.CreatedBy != "user-42" || v.CreatedByEmail != "alice@example.org" {
		t.Errorf("view = %+v", v)
	}

	run := h.getRun(v.Name)
	if run.Spec.DefinitionSnapshot == nil || len(run.Spec.DefinitionSnapshot.Steps) != 6 {
		t.Error("the API must snapshot the definition into the run")
	}
	if got := string(run.Spec.Parameters["entrypoints"].Raw); got != `[{"key":"main.py","schedule":"","type":"OnDemand"},{"key":"etl/load.py","schedule":"","type":"OnDemand"}]` {
		t.Errorf("parameters must be stored as sent, got %s", got)
	}
	if _, defaulted := run.Spec.Parameters["repoRef"]; defaulted {
		t.Error("defaults are applied when the run executes, not baked into the stored parameters")
	}
}

func TestCreateRunWithExplicitName(t *testing.T) {
	h := newHarness(t)
	h.seedPythonJob()
	body := nightly(nil)
	body["name"] = "my-run"

	expect(t, h.do(http.MethodPost, "/api/v1/runs", body), http.StatusCreated)
	if h.getRun("my-run").Spec.DefinitionRef.Name != "python-job" {
		t.Error("run not stored under the requested name")
	}
	rec := h.do(http.MethodPost, "/api/v1/runs", body)
	expect(t, rec, http.StatusConflict)
	if h.runCount() != 1 {
		t.Error("a duplicate name must not create a second run")
	}

	body["name"] = "Not_A_DNS_Name"
	rec = h.do(http.MethodPost, "/api/v1/runs", body)
	expect(t, rec, http.StatusBadRequest)
	if len(decode[errResp](t, rec).Details) == 0 {
		t.Error("an invalid name must explain why")
	}
}

func TestCreateRunValidation(t *testing.T) {
	h := newHarness(t)
	h.seedPythonJob()
	h.seedDefinition("broken", newBrokenDefinition()) // a definition with a typo'd step param

	cases := []struct {
		name       string
		body       any
		status     int
		wantDetail string
	}{
		{"malformed JSON", `{"definition": `, 400, ""},
		{"unknown field", `{"definition":"python-job","typo":1}`, 400, ""},
		{"no definition", map[string]any{"parameters": map[string]any{}}, 400, ""},
		{"unknown definition", map[string]any{"definition": "nope"}, 404, ""},
		{"invalid definition", map[string]any{"definition": "broken"}, 422, "unknown param"},
		{"missing required parameters", map[string]any{"definition": "python-job", "parameters": map[string]any{}}, 422, `"jobName" is required`},
		{"unknown parameter", nightly(map[string]any{"colour": "red"}), 422, "unknown parameter(s): colour"},
		{"wrong type", nightly(map[string]any{"entrypoints": "main.py"}), 422, "expected a list of objects"},
		{"pattern violation", nightly(map[string]any{"jobName": "Nightly Job"}), 422, "does not match pattern"},
		{"duplicate forEach items", nightly(map[string]any{"entrypoints": entrypointEntries("a.py", "a.py")}), 422, "is used twice"},
		{"empty forEach item", nightly(map[string]any{"entrypoints": []map[string]any{{"key": "", "type": "OnDemand", "schedule": ""}}}), 422, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/api/v1/runs", c.body)
			expect(t, rec, c.status)
			e := decode[errResp](t, rec)
			if e.Error == "" {
				t.Error("every error needs a message")
			}
			if c.wantDetail != "" && !strings.Contains(strings.Join(e.Details, "\n")+e.Error, c.wantDetail) {
				t.Errorf("want %q in %+v", c.wantDetail, e)
			}
		})
	}
	if h.runCount() != 0 {
		t.Errorf("no invalid request may create a run, found %d", h.runCount())
	}
}

func TestCreateRunReportsAllProblemsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.seedPythonJob()
	rec := h.do(http.MethodPost, "/api/v1/runs", map[string]any{"definition": "python-job", "parameters": map[string]any{"colour": "red"}})
	expect(t, rec, http.StatusUnprocessableEntity)
	joined := strings.Join(decode[errResp](t, rec).Details, "\n")
	for _, want := range []string{`"jobName" is required`, `"repoUrl" is required`, `"entrypoints" is required`, "colour"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details should mention %s:\n%s", want, joined)
		}
	}
}

// ---- read ----

func TestGetAndListRuns(t *testing.T) {
	h := newHarness(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name, def, by string, phase wizardv1.RunPhase, ageDays int) {
		h.seedRun(name, def, func(r *wizardv1.WizardRun) {
			r.Spec.CreatedBy = by
			r.Status.Phase = phase
			r.CreationTimestamp = metav1.NewTime(base.Add(time.Duration(ageDays) * 24 * time.Hour))
		})
	}
	mk("a", "python-job", "alice", wizardv1.RunReady, 1)
	mk("b", "python-job", "bob", wizardv1.RunFailed, 3)
	mk("c", "batch-job", "alice", wizardv1.RunReady, 2)
	mk("d", "python-job", "alice", "", 4) // never reconciled

	expect(t, h.do(http.MethodGet, "/api/v1/runs/nope", nil), http.StatusNotFound)
	if v := decode[runView](t, h.do(http.MethodGet, "/api/v1/runs/b", nil)); v.Name != "b" || v.Status.Phase != wizardv1.RunFailed || v.CreatedBy != "bob" {
		t.Errorf("get = %+v", v)
	}

	names := func(query string) string {
		t.Helper()
		rec := h.do(http.MethodGet, "/api/v1/runs"+query, nil)
		expect(t, rec, http.StatusOK)
		var out []string
		for _, it := range decode[struct {
			Items []runView `json:"items"`
		}](t, rec).Items {
			out = append(out, it.Name)
		}
		return strings.Join(out, ",")
	}
	for query, want := range map[string]string{
		"":                       "d,b,c,a", // newest first
		"?definition=python-job": "d,b,a",
		"?definition=batch-job":  "c",
		"?phase=Ready":           "c,a",
		"?phase=Pending":         "d", // a run nobody reconciled yet reads as Pending
		"?createdBy=alice":       "d,c,a",
		"?definition=python-job&phase=Ready&createdBy=alice": "a",
		"?limit=2": "d,b",
	} {
		if got := names(query); got != want {
			t.Errorf("GET /runs%s = %s, want %s", query, got, want)
		}
	}
	rec := h.do(http.MethodGet, "/api/v1/runs?limit=2", nil)
	if total := decode[struct {
		Total int `json:"total"`
	}](t, rec).Total; total != 4 {
		t.Errorf("total must count all matches, not the page: %d", total)
	}
	for _, bad := range []string{"?limit=0", "?limit=abc", "?limit=501"} {
		expect(t, h.do(http.MethodGet, "/api/v1/runs"+bad, nil), http.StatusBadRequest)
	}
}

// ---- retry / rollback / delete ----

func TestRetryRun(t *testing.T) {
	h := newHarness(t)
	h.seedRun("ok", "python-job", func(r *wizardv1.WizardRun) { r.Status.Phase = wizardv1.RunReady })
	h.seedRun("failed", "python-job", func(r *wizardv1.WizardRun) { r.Status.Phase = wizardv1.RunFailed })

	expect(t, h.do(http.MethodPost, "/api/v1/runs/nope/retry", nil), http.StatusNotFound)
	rec := h.do(http.MethodPost, "/api/v1/runs/ok/retry", nil)
	expect(t, rec, http.StatusConflict)
	if !strings.Contains(decode[errResp](t, rec).Error, "Ready") {
		t.Errorf("the conflict should name the phase: %s", rec.Body.String())
	}
	if _, set := h.getRun("ok").Annotations[wizardv1.AnnotationRetry]; set {
		t.Error("a run that is not failed must not be annotated")
	}

	for i := 0; i < 2; i++ { // idempotent
		expect(t, h.do(http.MethodPost, "/api/v1/runs/failed/retry", nil), http.StatusAccepted)
	}
	if h.getRun("failed").Annotations[wizardv1.AnnotationRetry] != "true" {
		t.Error("the operator's retry annotation must be set")
	}
}

func TestRollbackRun(t *testing.T) {
	h := newHarness(t)
	h.seedRun("a", "python-job", nil)

	expect(t, h.do(http.MethodPost, "/api/v1/runs/nope/rollback", nil), http.StatusNotFound)
	rec := h.do(http.MethodPost, "/api/v1/runs/a/rollback", nil)
	expect(t, rec, http.StatusAccepted)
	if decode[runView](t, rec).DesiredState != wizardv1.DesiredRolledBack || h.getRun("a").Spec.DesiredState != wizardv1.DesiredRolledBack {
		t.Error("desiredState must become RolledBack")
	}
	expect(t, h.do(http.MethodPost, "/api/v1/runs/a/rollback", nil), http.StatusOK) // already requested
}

func TestDeleteRun(t *testing.T) {
	h := newHarness(t)
	h.seedRun("plain", "python-job", nil)
	h.seedRun("provisioned", "python-job", func(r *wizardv1.WizardRun) {
		controllerutil.AddFinalizer(r, wizardv1.FinalizerRollback)
	})

	expect(t, h.do(http.MethodDelete, "/api/v1/runs/nope", nil), http.StatusNotFound)
	expect(t, h.do(http.MethodDelete, "/api/v1/runs/plain", nil), http.StatusNoContent) // nothing to roll back

	rec := h.do(http.MethodDelete, "/api/v1/runs/provisioned", nil)
	expect(t, rec, http.StatusAccepted) // the operator's finalizer rolls back first
	if !decode[runView](t, rec).Deleting {
		t.Error("the response must say the run is being deleted")
	}
	if h.getRun("provisioned").DeletionTimestamp.IsZero() {
		t.Error("the run must be marked for deletion")
	}
}

func TestBulkRollback(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"p1", "p2", "p3"} {
		h.seedRun(n, "python-job", nil)
	}
	h.seedRun("b1", "batch-job", nil)

	post := func(body any) (*httptest.ResponseRecorder, bulkRollbackResponse) {
		rec := h.do(http.MethodPost, "/api/v1/runs/bulk-rollback", body)
		return rec, decode[bulkRollbackResponse](t, rec)
	}
	rolledBack := func(n string) bool { return h.getRun(n).Spec.DesiredState == wizardv1.DesiredRolledBack }

	// An empty request must never mean "everything".
	expect(t, h.do(http.MethodPost, "/api/v1/runs/bulk-rollback", map[string]any{}), http.StatusBadRequest)
	for _, n := range []string{"p1", "p2", "p3", "b1"} {
		if rolledBack(n) {
			t.Fatalf("%s was rolled back by an empty selector", n)
		}
	}

	rec, resp := post(map[string]any{"names": []string{"p1", "p1", "ghost", "b1"}, "definition": "python-job"})
	expect(t, rec, http.StatusAccepted)
	if strings.Join(resp.Accepted, ",") != "p1" || len(resp.Failed) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	failed := map[string]string{}
	for _, f := range resp.Failed {
		failed[f.Name] = f.Error
	}
	if !strings.Contains(failed["ghost"], "not found") || !strings.Contains(failed["b1"], "different definition") {
		t.Errorf("failures = %v", failed)
	}
	if !rolledBack("p1") || rolledBack("b1") || rolledBack("p2") {
		t.Error("only the selected, matching run may be rolled back")
	}

	rec, resp = post(map[string]any{"definition": "python-job"})
	expect(t, rec, http.StatusAccepted)
	if strings.Join(resp.Accepted, ",") != "p1,p2,p3" || !rolledBack("p3") || rolledBack("b1") {
		t.Errorf("definition selector: %+v", resp)
	}

	rec, resp = post(map[string]any{"names": []string{"ghost"}})
	expect(t, rec, http.StatusOK) // nothing accepted
	if len(resp.Accepted) != 0 || len(resp.Failed) != 1 || resp.Accepted == nil {
		t.Errorf("all-failed response = %+v (accepted must be [], not null)", resp)
	}
}

func TestBulkRollbackRefusesHugeSelections(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < maxBulkTargets+1; i++ {
		h.seedRun(fmt.Sprintf("run-%03d", i), "python-job", nil)
	}
	rec := h.do(http.MethodPost, "/api/v1/runs/bulk-rollback", map[string]any{"definition": "python-job"})
	expect(t, rec, http.StatusBadRequest)
	if h.getRun("run-000").Spec.DesiredState == wizardv1.DesiredRolledBack {
		t.Error("a refused selection must not roll anything back")
	}
}

// ---- definitions and resources ----

func TestDefinitions(t *testing.T) {
	h := newHarness(t)
	h.seedDefinition("python-job", newGoodDefinition())
	h.seedDefinition("broken", newBrokenDefinition())

	rec := h.do(http.MethodGet, "/api/v1/definitions", nil)
	expect(t, rec, http.StatusOK)
	items := decode[struct {
		Items []definitionView `json:"items"`
	}](t, rec).Items
	if len(items) != 2 || items[0].Name != "broken" || items[1].Name != "python-job" {
		t.Fatalf("items must be sorted by name: %+v", items)
	}
	if items[0].Valid || items[0].Error == "" {
		t.Errorf("a broken definition must be flagged with a reason: %+v", items[0])
	}
	if !items[1].Valid || len(items[1].Spec.Parameters) != 5 {
		t.Errorf("the good definition must expose its parameter schema: %+v", items[1])
	}

	if v := decode[definitionView](t, h.do(http.MethodGet, "/api/v1/definitions/python-job", nil)); v.Name != "python-job" || !v.Valid || v.Spec.Steps[5].ForEach == "" {
		t.Errorf("get = %+v", v)
	}
	expect(t, h.do(http.MethodGet, "/api/v1/definitions/nope", nil), http.StatusNotFound)

	empty := newHarness(t)
	rec = empty.do(http.MethodGet, "/api/v1/definitions", nil)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("an empty list must serialise as [], got %s", rec.Body.String())
	}
}

func TestListResources(t *testing.T) {
	h := newHarness(t)
	led := ledger.New(h.c, ns)
	ctx := context.Background()
	refA := wizardv1.ResourceReference{Run: "run-a", Step: "chain"}
	refB := wizardv1.ResourceReference{Run: "run-b", Step: "chain"}
	if _, err := led.Register(ctx, ledger.Key{Service: wizardv1.ServiceWeave, Kind: "chain", Name: "nightly"}, refA, ledger.NewEntry{Managed: true, SpecHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := led.AddRef(ctx, ledger.Key{Service: wizardv1.ServiceWeave, Kind: "chain", Name: "nightly"}, refB, ledger.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := led.Register(ctx, ledger.Key{Service: wizardv1.ServiceForge, Kind: "gitwatcher", Name: "nightly"}, refA, ledger.NewEntry{Managed: false}); err != nil {
		t.Fatal(err)
	}

	list := func(query string) []resourceView {
		rec := h.do(http.MethodGet, "/api/v1/resources"+query, nil)
		expect(t, rec, http.StatusOK)
		return decode[struct {
			Items []resourceView `json:"items"`
		}](t, rec).Items
	}
	all := list("")
	if len(all) != 2 || all[0].Service != wizardv1.ServiceForge || all[1].Kind != "chain" {
		t.Fatalf("items must be sorted by service/kind/name: %+v", all)
	}
	if len(all[1].Refs) != 2 || !all[1].Managed || all[0].Managed {
		t.Errorf("a shared resource must list every owner and its managed flag: %+v", all)
	}
	if got := list("?service=weave"); len(got) != 1 || got[0].Kind != "chain" {
		t.Errorf("service filter: %+v", got)
	}
	if got := list("?kind=gitwatcher"); len(got) != 1 || got[0].Service != wizardv1.ServiceForge {
		t.Errorf("kind filter: %+v", got)
	}
	if got := list("?service=index"); got == nil || len(got) != 0 {
		t.Errorf("no match must be an empty list, got %+v", got)
	}
}

func TestPanicIsRecoveredAsA500(t *testing.T) {
	h := recoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	expect(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("the panic value must not reach the client: %s", rec.Body.String())
	}
}
