// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"flag"
	"strings"

	"fusion-platform.io/fusion-wizard/internal/apiserver"
	"fusion-platform.io/fusion-wizard/internal/envutil"
)

// options is the API server config plus the logging settings, which only main needs.
type options struct {
	apiserver.Config
	LogLevel  string
	LogFormat string
}

// optionsFromFlags reads flags whose defaults come from the environment, so the Helm chart can
// configure the server with env vars alone.
func optionsFromFlags() options {
	var (
		o          options
		allowedSAs string
	)
	flag.StringVar(&o.Addr, "addr", envutil.String("API_ADDR", ":8083"), "TCP address to listen on.")
	flag.StringVar(&o.Namespace, "namespace", envutil.String("NAMESPACE", "fusion"), "Namespace whose runs, definitions and ledger the API serves.")
	flag.StringVar(&allowedSAs, "allowed-sa", strings.Join(envutil.List("AUTH_ALLOWED_SA"), ","), "Comma-separated <namespace>/<name> service accounts allowed to call the API (the BFFs).")
	flag.StringVar(&o.AuthAudience, "auth-audience", envutil.String("AUTH_AUDIENCE", ""), "Audience the caller's token must be valid for (optional).")
	flag.BoolVar(&o.AllowUnauthenticated, "allow-unauthenticated", envutil.Bool("ALLOW_UNAUTHENTICATED"), "Skip authentication. Local development only.")
	flag.StringVar(&o.LogLevel, "log-level", envutil.String("LOG_LEVEL", "info"), "Log level: debug|info|warn|error.")
	flag.StringVar(&o.LogFormat, "log-format", envutil.String("LOG_FORMAT", "json"), "Log format: json|text.")
	flag.Parse()

	o.AllowedServiceAccounts = envutil.Split(allowedSAs)
	return o
}
