// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package apiserver implements the fusion-wizard REST API. It only reads and writes custom
// resources (WizardRun, WizardDefinition, WizardResource); the operator does all the provisioning.
// Callers are the BFFs, authenticated as Kubernetes service accounts.
package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Config is the API server configuration.
type Config struct {
	// Addr is the TCP address to listen on, e.g. ":8083".
	Addr string
	// Namespace is the only namespace the API serves.
	Namespace string
	// AllowedServiceAccounts lists the callers ("<namespace>/<name>") that may use the API. The BFFs
	// go here. There is deliberately no "any valid service account" mode: this API can delete
	// infrastructure, so every caller is named.
	AllowedServiceAccounts []string
	// AuthAudience, if set, is the audience the caller's token must be valid for.
	AuthAudience string
	// AllowUnauthenticated skips authentication entirely. For local development only.
	AllowUnauthenticated bool
}

// Validate refuses configurations that would leave the API open by accident.
func (c Config) Validate() error {
	var problems []string
	if c.Namespace == "" {
		problems = append(problems, "namespace is required")
	}
	if !c.AllowUnauthenticated && len(c.AllowedServiceAccounts) == 0 {
		problems = append(problems, "set AUTH_ALLOWED_SA (comma-separated <namespace>/<name>) or, for local development only, ALLOW_UNAUTHENTICATED=true")
	}
	for _, sa := range c.AllowedServiceAccounts {
		if ns, name, ok := strings.Cut(sa, "/"); !ok || ns == "" || name == "" || strings.Contains(name, "/") {
			problems = append(problems, fmt.Sprintf("allowed service account %q must look like <namespace>/<name>", sa))
		}
	}
	if len(problems) > 0 {
		return errors.New("invalid API server config: " + strings.Join(problems, "; "))
	}
	return nil
}

// Server is the REST API server.
type Server struct {
	cfg     Config
	client  client.Client
	authn   Authenticator
	allowed map[string]struct{}
}

// New builds a Server that authenticates callers with Kubernetes TokenReview.
func New(cfg Config, c client.Client, kc kubernetes.Interface) (*Server, error) {
	var authn Authenticator
	if !cfg.AllowUnauthenticated {
		if kc == nil {
			return nil, errors.New("a Kubernetes clientset is required for token review")
		}
		var audiences []string
		if cfg.AuthAudience != "" {
			audiences = []string{cfg.AuthAudience}
		}
		authn = &TokenReviewAuthenticator{KubeClient: kc, Audiences: audiences}
	}
	return newServer(cfg, c, authn)
}

func newServer(cfg Config, c client.Client, authn Authenticator) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, client: c, authn: authn, allowed: map[string]struct{}{}}
	for _, sa := range cfg.AllowedServiceAccounts {
		s.allowed[sa] = struct{}{}
	}
	return s, nil
}

// Start serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
