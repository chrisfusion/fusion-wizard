// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/steps"
	"fusion-platform.io/fusion-wizard/internal/upstream"
)

// EnvFactory builds the step environment for one reconcile. Production reads the instance config
// fresh each time, so a changed ConfigMap takes effect without restarting the operator; tests
// return an environment wired to fakes.
type EnvFactory interface {
	Env(ctx context.Context) (*steps.Env, error)
}

// ConfigEnvFactory is the production EnvFactory.
type ConfigEnvFactory struct {
	// Reader must read the ConfigMap uncached (mgr.GetAPIReader()), which needs only "get" RBAC.
	Reader    client.Reader
	Namespace string
	ConfigMap string
	Ledger    *ledger.Ledger

	// One token source per upstream: services validate different audiences (or none, for weave).
	ForgeTokens, IndexTokens, WeaveTokens upstream.TokenSource
}

func (f *ConfigEnvFactory) Env(ctx context.Context) (*steps.Env, error) {
	cfg, err := instancecfg.Load(ctx, f.Reader, f.Namespace, f.ConfigMap)
	if err != nil {
		return nil, err
	}
	return &steps.Env{
		Cfg:    cfg,
		Forge:  upstream.NewForge(cfg.ForgeURL, f.ForgeTokens),
		Index:  upstream.NewIndex(cfg.IndexURL, f.IndexTokens),
		Weave:  upstream.NewWeave(cfg.WeaveURL, f.WeaveTokens),
		Ledger: f.Ledger,
	}, nil
}

// Sweeper periodically finishes ledger deletions that a crash interrupted: an entry marked
// Terminating with no references has nobody responsible for it. It runs only on the leader.
type Sweeper struct {
	Envs     EnvFactory
	Interval time.Duration
}

func (s *Sweeper) NeedLeaderElection() bool { return true }

// Start blocks until ctx is cancelled.
func (s *Sweeper) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("sweeper")
	tick := time.NewTicker(s.Interval)
	defer tick.Stop()
	for {
		if err := s.sweep(ctx); err != nil {
			log.Error(err, "sweeping interrupted deletions failed; will retry")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) error {
	env, err := s.Envs.Env(ctx)
	if err != nil {
		return fmt.Errorf("instance config: %w", err)
	}
	return env.SweepTerminating(ctx)
}
