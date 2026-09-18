// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"fusion-platform.io/fusion-wizard/internal/steps/stepstest"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	ns          = "fusion"
	bffToken    = "bff-token"
	intruderTok = "intruder-token"
)

// fakeAuthn maps tokens to principals; an unknown token is "not authenticated".
type fakeAuthn struct {
	principals map[string]*Principal
	err        error
}

func (f fakeAuthn) Authenticate(_ context.Context, token string) (*Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.principals[token], nil
}

type harness struct {
	t *testing.T
	s *Server
	c client.Client
	h http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := wizardv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&wizardv1.WizardRun{}).Build()
	srv, err := newServer(Config{Namespace: ns, AllowedServiceAccounts: []string{"fusion/fusion-bff"}}, c, fakeAuthn{
		principals: map[string]*Principal{
			bffToken:    {Namespace: "fusion", Name: "fusion-bff"},
			intruderTok: {Namespace: "fusion", Name: "intruder"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, s: srv, c: c, h: srv.Handler()}
}

// do sends a request as the allowlisted BFF. body may be a string (sent raw) or any JSON value.
func (h *harness) do(method, path string, body any) *httptest.ResponseRecorder {
	return h.doWith(method, path, body, map[string]string{"Authorization": "Bearer " + bffToken})
}

func (h *harness) doWith(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not the expected JSON (%v): %s", err, rec.Body.String())
	}
	return v
}

// expect fails the test unless the response has the given status.
func expect(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
}

func (h *harness) seedDefinition(name string, spec *wizardv1.WizardDefinitionSpec) {
	h.t.Helper()
	d := &wizardv1.WizardDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: *spec}
	if err := h.c.Create(context.Background(), d); err != nil {
		h.t.Fatal(err)
	}
}

// seedPythonJob creates the reference definition under the name "python-job".
func (h *harness) seedPythonJob() { h.seedDefinition("python-job", stepstest.PythonJob()) }

func (h *harness) getRun(name string) *wizardv1.WizardRun {
	h.t.Helper()
	var run wizardv1.WizardRun
	if err := h.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &run); err != nil {
		h.t.Fatal(err)
	}
	return &run
}

func (h *harness) runCount() int {
	h.t.Helper()
	var list wizardv1.WizardRunList
	if err := h.c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		h.t.Fatal(err)
	}
	return len(list.Items)
}

// seedRun stores a run directly (bypassing the API), for list/filter/rollback tests.
func (h *harness) seedRun(name, definition string, mutate func(*wizardv1.WizardRun)) {
	h.t.Helper()
	run := &wizardv1.WizardRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       wizardv1.WizardRunSpec{DefinitionRef: corev1.LocalObjectReference{Name: definition}, DesiredState: wizardv1.DesiredApplied},
	}
	if mutate != nil {
		mutate(run)
	}
	if err := h.c.Create(context.Background(), run); err != nil {
		h.t.Fatal(err)
	}
	if run.Status.Phase != "" { // the status subresource ignores status on create; write it explicitly
		if err := h.c.Status().Update(context.Background(), run); err != nil {
			h.t.Fatal(err)
		}
	}
}

// newGoodDefinition and newBrokenDefinition return the reference definition and a copy with a
// typo'd step param, which ValidateDefinition rejects.
func newGoodDefinition() *wizardv1.WizardDefinitionSpec { return stepstest.PythonJob() }

func newBrokenDefinition() *wizardv1.WizardDefinitionSpec {
	d := stepstest.PythonJob()
	d.Steps[0].Params["repoUrll"] = "typo"
	return d
}
