// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package upstream holds minimal REST clients for the services the wizard orchestrates: forge,
// index and weave. Each call authenticates with the operator's own service-account token, which
// is re-read on every request because the kubelet rotates projected tokens.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	maxErrorBody   = 64 << 10
	maxMessageLen  = 300
)

// Sentinel errors, matched with errors.Is against an *APIError.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// TokenSource supplies the bearer token for one request.
type TokenSource interface {
	Token() (string, error)
}

// FileTokenSource reads a projected service-account token from disk on every call and never
// caches it. An empty Path means "no authentication" (upstream auth disabled, local dev).
type FileTokenSource struct{ Path string }

func (f FileTokenSource) Token() (string, error) {
	if f.Path == "" {
		return "", nil
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// StaticToken is a fixed token, for tests.
type StaticToken string

func (s StaticToken) Token() (string, error) { return string(s), nil }

// APIError is a failed upstream call. Status is 0 when no HTTP response was received (transport
// failure, token read failure). Error() deliberately omits the underlying transport error: those
// messages contain internal cluster DNS names, and run status is readable by API clients. Detail()
// has the full text for operator logs.
type APIError struct {
	Service string
	Method  string
	Path    string
	Status  int
	Message string
	Err     error
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s %s: %s", e.Service, e.Method, e.Path, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("%s: %s %s: HTTP %d", e.Service, e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s: %s %s: HTTP %d: %s", e.Service, e.Method, e.Path, e.Status, e.Message)
}

// Detail is Error() plus the underlying cause; log it, never return it to API clients.
func (e *APIError) Detail() string {
	if e.Err == nil {
		return e.Error()
	}
	return e.Error() + ": " + e.Err.Error()
}

func (e *APIError) Unwrap() error { return e.Err }

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	}
	return false
}

// IsTransient reports whether retrying the same call later may succeed: no response at all,
// throttling, or a server-side failure. 4xx responses other than 429 are permanent.
func IsTransient(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == 0 || ae.Status == http.StatusTooManyRequests || ae.Status >= 500
}

// Client is the shared JSON-over-HTTP core of the service clients.
type Client struct {
	service string
	base    string
	http    *http.Client
	tokens  TokenSource
}

func newClient(service, baseURL string, tokens TokenSource) *Client {
	return &Client{
		service: service,
		base:    strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
		tokens:  tokens,
	}
}

// do sends one request. in (if non-nil) is JSON-encoded as the body; a 2xx response body is
// decoded into out (if non-nil). Non-2xx responses become *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	fail := func(msg string, err error) error {
		return &APIError{Service: c.service, Method: method, Path: path, Message: msg, Err: err}
	}

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fail("encoding request body failed", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fail("building request failed", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.tokens != nil {
		token, err := c.tokens.Token()
		if err != nil {
			return fail("reading service account token failed", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fail("request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &APIError{Service: c.service, Method: method, Path: path, Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fail("decoding response failed", err)
	}
	return nil
}

// errorMessage extracts the message from the error shapes used across the platform:
// {"error": "..."} (forge, index, bff) and {"code": n, "message": "..."} (weave).
func errorMessage(raw []byte) string {
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil {
		msg = body.Error
		if msg == "" {
			msg = body.Message
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if len(msg) > maxMessageLen {
		msg = msg[:maxMessageLen] + "..."
	}
	return msg
}

// ignoreNotFound makes deletes idempotent: rollback must be safe to repeat, so a resource that is
// already gone counts as deleted.
func ignoreNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
