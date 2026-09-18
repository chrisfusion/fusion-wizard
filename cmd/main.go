// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Command manager is the fusion-wizard operator: it reconciles WizardRuns and sweeps interrupted
// ledger deletions. The REST API is a separate binary (cmd/api).
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"fusion-platform.io/fusion-wizard/internal/controller"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wizardv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr   string
		probeAddr     string
		namespace     string
		configMap     string
		leaderElect   bool
		sweepInterval time.Duration
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the metrics endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the health probe endpoint binds to")
	flag.StringVar(&namespace, "namespace", envOr("NAMESPACE", "fusion"), "the only namespace this operator watches")
	flag.StringVar(&configMap, "config-map", envOr("INSTANCE_CONFIG_MAP", "fusion-wizard-config"), "ConfigMap holding the per-instance settings")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election (needed with more than one replica)")
	flag.DurationVar(&sweepInterval, "sweep-interval", time.Minute, "how often interrupted ledger deletions are finished")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "wizard.fusion-platform.io",
		// A namespaced Role is enough: the operator never reads outside its own namespace.
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	envs := &controller.ConfigEnvFactory{
		Reader:    mgr.GetAPIReader(), // uncached: the ConfigMap needs only "get", not list/watch
		Namespace: namespace,
		ConfigMap: configMap,
		Ledger:    ledger.New(mgr.GetClient(), namespace),
		// Empty path = no authentication (upstream auth disabled, local dev). Each upstream may
		// validate a different audience, hence one projected token per service.
		ForgeTokens: upstream.FileTokenSource{Path: os.Getenv("FORGE_TOKEN_PATH")},
		IndexTokens: upstream.FileTokenSource{Path: os.Getenv("INDEX_TOKEN_PATH")},
		WeaveTokens: upstream.FileTokenSource{Path: os.Getenv("WEAVE_TOKEN_PATH")},
	}

	if err := (&controller.WizardRunReconciler{Client: mgr.GetClient(), Envs: envs}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create the WizardRun controller")
		os.Exit(1)
	}
	if err := mgr.Add(&controller.Sweeper{Envs: envs, Interval: sweepInterval}); err != nil {
		setupLog.Error(err, "unable to add the ledger sweeper")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up the ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "namespace", namespace, "configMap", configMap)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited with an error")
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
