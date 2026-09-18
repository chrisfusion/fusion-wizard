// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serve starts a test server and returns its URL.
func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFileTokenSourceRereadsEveryCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	src := FileTokenSource{Path: path}

	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := src.Token(); err != nil || got != "first" {
		t.Fatalf("Token() = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := src.Token(); got != "rotated" {
		t.Errorf("a rotated token must be picked up immediately, got %q", got)
	}
	if got, err := (FileTokenSource{}).Token(); err != nil || got != "" {
		t.Errorf("empty path must mean no auth, got %q, %v", got, err)
	}
}

func TestBearerTokenSentPerRequest(t *testing.T) {
	var seen []string
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	path := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(path, []byte("t1"), 0o600)
	c := newClient("svc", url, FileTokenSource{Path: path})

	_ = c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	_ = os.WriteFile(path, []byte("t2"), 0o600)
	_ = c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)

	if len(seen) != 2 || seen[0] != "Bearer t1" || seen[1] != "Bearer t2" {
		t.Errorf("Authorization headers = %v", seen)
	}

	seen = nil
	anon := newClient("svc", url, FileTokenSource{})
	_ = anon.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if len(seen) != 1 || seen[0] != "" {
		t.Errorf("no token configured must send no Authorization header, got %v", seen)
	}
}

func TestTokenReadFailureIsTransientAPIError(t *testing.T) {
	c := newClient("svc", "http://unused", FileTokenSource{Path: filepath.Join(t.TempDir(), "missing")})
	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 0 || !IsTransient(err) {
		t.Fatalf("want a status-0 transient APIError, got %v", err)
	}
}

func TestErrorMessages(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantMsg string
	}{
		{"forge/index shape", 422, `{"error":"repo unreachable"}`, "repo unreachable"},
		{"weave shape", 400, `{"code":400,"message":"bad chain"}`, "bad chain"},
		{"plain text", 500, "boom\n", "boom"},
		{"empty", 502, "", ""},
		{"truncated", 500, strings.Repeat("x", 1000), strings.Repeat("x", maxMessageLen) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			err := newClient("svc", url, nil).do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != tc.wantMsg {
				t.Fatalf("got %+v (%v)", ae, err)
			}
		})
	}
}

func TestSentinelMatching(t *testing.T) {
	status := 0
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	c := newClient("svc", url, nil)

	status = http.StatusNotFound
	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || IsTransient(err) {
		t.Errorf("404: NotFound=%v Conflict=%v transient=%v", errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict), IsTransient(err))
	}
	status = http.StatusConflict
	err = c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if !errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || IsTransient(err) {
		t.Errorf("409 mismatch")
	}
	for _, s := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		status = s
		if err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !IsTransient(err) {
			t.Errorf("HTTP %d must be transient", s)
		}
	}
	status = http.StatusForbidden
	if err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil); IsTransient(err) {
		t.Error("HTTP 403 must be permanent")
	}
	if IsTransient(errors.New("not an api error")) {
		t.Error("foreign errors are not transient")
	}
}

func TestTransportErrorHidesInternalAddress(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close() // nothing listens any more

	err := newClient("svc", dead, nil).do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 0 || !IsTransient(err) {
		t.Fatalf("want a transient status-0 error, got %v", err)
	}
	host := strings.TrimPrefix(dead, "http://")
	if strings.Contains(ae.Error(), host) {
		t.Errorf("Error() leaks the upstream address: %s", ae.Error())
	}
	if !strings.Contains(ae.Detail(), host) {
		t.Errorf("Detail() should keep the cause for logs: %s", ae.Detail())
	}
}

func TestRequestBodyAndDecoding(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var in map[string]string
		_ = decodeBody(r, &in)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"echo":"` + in["k"] + `"}`))
	})
	var out struct{ Echo string }
	err := newClient("svc", url, nil).do(context.Background(), http.MethodPost, "/x", nil, map[string]string{"k": "v"}, &out)
	if err != nil || out.Echo != "v" {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestContextCancellation(t *testing.T) {
	url := serve(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newClient("svc", url, nil).do(ctx, http.MethodGet, "/x", nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context must stay visible through the APIError, got %v", err)
	}
}
