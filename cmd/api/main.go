// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Command api-server is the fusion-wizard REST API. It only reads and writes custom resources; the
// operator (cmd/) does the provisioning.
package main

import (
	"log/slog"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-wizard/internal/apiserver"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

func main() {
	opts := optionsFromFlags()
	setupLogger(opts.LogLevel, opts.LogFormat) // first, so every later line is structured

	if err := opts.Validate(); err != nil {
		fatal("invalid configuration", err)
	}
	if opts.AllowUnauthenticated {
		slog.Warn("authentication is DISABLED: every caller may create, roll back and delete runs; use for local development only")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wizardv1.AddToScheme(scheme))

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		fatal("cannot load the Kubernetes config", err)
	}
	// A direct, uncached client: the API does one-off reads and writes and needs no informers.
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		fatal("cannot create the Kubernetes client", err)
	}
	var kc kubernetes.Interface
	if !opts.AllowUnauthenticated {
		if kc, err = kubernetes.NewForConfig(restCfg); err != nil {
			fatal("cannot create the Kubernetes clientset", err)
		}
	}

	srv, err := apiserver.New(opts.Config, c, kc)
	if err != nil {
		fatal("cannot create the API server", err)
	}
	slog.Info("starting API server", "addr", opts.Addr, "namespace", opts.Namespace, "allowed_service_accounts", opts.AllowedServiceAccounts)
	if err := srv.Start(ctrl.SetupSignalHandler()); err != nil {
		fatal("API server stopped", err)
	}
}

func fatal(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1) // slog has no Fatal
}

// setupLogger installs the default slog logger. An unknown level or format falls back to the
// default (info, json) with a warning instead of refusing to start.
func setupLogger(level, format string) {
	lvl, levelOK := parseLevel(level)
	hopts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, hopts)
	formatOK := true
	switch strings.ToLower(format) {
	case "json", "":
	case "text":
		h = slog.NewTextHandler(os.Stdout, hopts)
	default:
		formatOK = false
	}
	slog.SetDefault(slog.New(h))
	if !levelOK {
		slog.Warn("unknown log level, using info", "log_level", level)
	}
	if !formatOK {
		slog.Warn("unknown log format, using json", "log_format", format)
	}
}

func parseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, true
	case "info", "":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}
