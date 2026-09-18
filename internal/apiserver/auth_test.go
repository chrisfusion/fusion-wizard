// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// reviewRig is a clientset whose TokenReview answers come from a table, recording what was asked.
type reviewRig struct {
	cs       *fake.Clientset
	reviewed []authv1.TokenReviewSpec
	users    map[string]string // token -> username; unknown tokens are unauthenticated
	fail     error
}

func newReviewRig() *reviewRig {
	r := &reviewRig{cs: fake.NewSimpleClientset(), users: map[string]string{}}
	r.cs.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authv1.TokenReview)
		r.reviewed = append(r.reviewed, tr.Spec)
		if r.fail != nil {
			return true, nil, r.fail
		}
		user, ok := r.users[tr.Spec.Token]
		out := tr.DeepCopy()
		out.Status = authv1.TokenReviewStatus{Authenticated: ok, User: authv1.UserInfo{Username: user}}
		return true, out, nil
	})
	return r
}

func TestAuthenticationFlow(t *testing.T) {
	rig := newReviewRig()
	rig.users["bff-jwt"] = "system:serviceaccount:fusion:fusion-bff"
	rig.users["stranger-jwt"] = "system:serviceaccount:fusion:stranger"
	rig.users["human-jwt"] = "alice"
	rig.users["odd-jwt"] = "system:serviceaccount:fusion" // malformed service account name

	h := newHarness(t)
	srv, err := New(Config{Namespace: ns, AllowedServiceAccounts: []string{"fusion/fusion-bff"}}, h.c, rig.cs)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	call := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name string
		auth string
		want int
	}{
		{"no header", "", 401},
		{"wrong scheme", "Basic abc", 401},
		{"empty bearer", "Bearer ", 401},
		{"token not authenticated", "Bearer unknown-jwt", 401},
		{"authenticated human, not a service account", "Bearer human-jwt", 401},
		{"malformed service account name", "Bearer odd-jwt", 401},
		{"service account not on the allowlist", "Bearer stranger-jwt", 403},
		{"allowlisted service account", "Bearer bff-jwt", 200},
		{"scheme is case-insensitive", "bearer bff-jwt", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(c.auth)
			expect(t, rec, c.want)
			if c.want == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Error("a 401 must carry WWW-Authenticate: Bearer")
			}
			if c.want != 200 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("errors must use the platform's {\"error\"} shape, got %s", rec.Body.String())
			}
		})
	}
	if len(rig.reviewed) == 0 || rig.reviewed[len(rig.reviewed)-1].Token != "bff-jwt" {
		t.Errorf("the caller's token must be what is reviewed: %+v", rig.reviewed)
	}
}

func TestTokenReviewFailureIsA503WithoutLeakingDetails(t *testing.T) {
	rig := newReviewRig()
	rig.fail = errors.New("dial tcp 10.96.0.1:443: connection refused")
	h := newHarness(t)
	srv, err := New(Config{Namespace: ns, AllowedServiceAccounts: []string{"fusion/fusion-bff"}}, h.c, rig.cs)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	expect(t, rec, http.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.96") {
		t.Errorf("an internal address leaked into the response: %s", rec.Body.String())
	}
}

func TestAudienceIsPassedToTokenReview(t *testing.T) {
	rig := newReviewRig()
	rig.users["t"] = "system:serviceaccount:fusion:fusion-bff"
	h := newHarness(t)
	srv, err := New(Config{Namespace: ns, AllowedServiceAccounts: []string{"fusion/fusion-bff"}, AuthAudience: "fusion-wizard"}, h.c, rig.cs)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer t")
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if len(rig.reviewed) != 1 || len(rig.reviewed[0].Audiences) != 1 || rig.reviewed[0].Audiences[0] != "fusion-wizard" {
		t.Errorf("reviewed = %+v", rig.reviewed)
	}
}

func TestProbesNeedNoAuthentication(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		expect(t, h.doWith(http.MethodGet, path, nil, nil), http.StatusOK)
	}
	expect(t, h.doWith(http.MethodGet, "/api/v1/runs", nil, nil), http.StatusUnauthorized)
}

func TestUnauthenticatedDevMode(t *testing.T) {
	h := newHarness(t)
	srv, err := New(Config{Namespace: ns, AllowUnauthenticated: true}, h.c, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.h = srv.Handler()
	expect(t, h.doWith(http.MethodGet, "/api/v1/runs", nil, nil), http.StatusOK)
}

func TestUserIdentityHeaders(t *testing.T) {
	h := newHarness(t)
	h.seedPythonJob()
	body := map[string]any{"definition": "python-job", "parameters": map[string]any{
		"jobName": "nightly", "repoUrl": "https://g/x.git", "entrypoints": []string{"main.py"}}}
	auth := map[string]string{"Authorization": "Bearer " + bffToken}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range auth {
			m[kk] = vv
		}
		m[k] = v
		return m
	}

	rec := h.doWith(http.MethodPost, "/api/v1/runs", body, map[string]string{
		"Authorization": auth["Authorization"], "X-User-ID": "  user-42 ", "X-User-Email": "alice@example.org"})
	expect(t, rec, http.StatusCreated)
	run := h.getRun(decode[runView](t, rec).Name)
	if run.Spec.CreatedBy != "user-42" || run.Spec.CreatedByEmail != "alice@example.org" {
		t.Errorf("identity forwarded by the BFF must be stamped on the run: %+v", run.Spec)
	}

	// Rejected values: too long, control characters. The run is not created.
	before := h.runCount()
	for _, bad := range []map[string]string{
		with("X-User-ID", strings.Repeat("u", maxHeaderLen+1)),
		with("X-User-ID", "user\x00id"),
		with("X-User-Email", "a@b\r\nX-Injected: 1"),
	} {
		if rec := h.doWith(http.MethodPost, "/api/v1/runs", body, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("headers %v: status %d, want 400", bad, rec.Code)
		}
	}
	if h.runCount() != before {
		t.Error("a request with an invalid identity header must not create a run")
	}

	// A caller that is not allowlisted never gets to vouch for a user: it is refused before any
	// header is read, so a spoofed identity cannot reach the run.
	before = h.runCount()
	rec = h.doWith(http.MethodPost, "/api/v1/runs", body, map[string]string{"Authorization": "Bearer " + intruderTok, "X-User-ID": "admin"})
	expect(t, rec, http.StatusForbidden)
	if h.runCount() != before {
		t.Error("a forbidden caller must not create anything")
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{Namespace: "fusion", AllowedServiceAccounts: []string{"fusion/fusion-bff"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := (Config{Namespace: "fusion", AllowUnauthenticated: true}).Validate(); err != nil {
		t.Errorf("explicit dev mode must be accepted: %v", err)
	}

	bad := map[string]Config{
		"no auth configured":         {Namespace: "fusion"},
		"no namespace":               {AllowedServiceAccounts: []string{"fusion/x"}},
		"service account w/o slash":  {Namespace: "fusion", AllowedServiceAccounts: []string{"fusion-bff"}},
		"service account empty name": {Namespace: "fusion", AllowedServiceAccounts: []string{"fusion/"}},
		"service account extra part": {Namespace: "fusion", AllowedServiceAccounts: []string{"a/b/c"}},
	}
	for name, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// The open-by-accident case names both ways out.
	if err := (Config{Namespace: "fusion"}).Validate(); err == nil || !strings.Contains(err.Error(), "AUTH_ALLOWED_SA") {
		t.Errorf("the message must say how to fix it: %v", err)
	}

	h := newHarness(t)
	if _, err := New(ok, h.c, nil); err == nil {
		t.Error("New without a clientset must fail when authentication is on")
	}
}

func TestTokenReviewAuthenticatorDirect(t *testing.T) {
	rig := newReviewRig()
	rig.users["t"] = "system:serviceaccount:team-a:runner"
	a := &TokenReviewAuthenticator{KubeClient: rig.cs}
	p, err := a.Authenticate(context.Background(), "t")
	if err != nil || p == nil || p.String() != "team-a/runner" {
		t.Fatalf("principal = %+v, err = %v", p, err)
	}
	if p, err := a.Authenticate(context.Background(), "nope"); p != nil || err != nil {
		t.Errorf("an unknown token is (nil, nil), got %+v, %v", p, err)
	}
}
