// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package steps is the wizard's step catalogue: the fixed set of provisioning actions a
// WizardDefinition can select from, each with an idempotent Ensure and an undo that runs through
// the ledger. The package has no Kubernetes controller code; the run reconciler drives it.
package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// Resource kinds as recorded in the ledger and in run status.
const (
	KindGitWatcher  = "gitwatcher"
	KindArtifact    = "artifact"
	KindTag         = "tag"
	KindJobTemplate = "jobtemplate"
	KindChain       = "chain"
	KindTrigger     = "trigger"
)

// retryTerminating is how soon a step retries when the resource it wants is still being deleted
// by a previous owner.
const retryTerminating = 2 * time.Second

// The upstream calls the steps need, declared here (consumer side) so tests can substitute fakes.
// *upstream.ForgeClient, *upstream.IndexClient and *upstream.WeaveClient satisfy them.
type (
	Forge interface {
		GetGitWatcher(ctx context.Context, name string) (*upstream.GitWatcher, error)
		CreateGitWatcher(ctx context.Context, req upstream.CreateGitWatcherRequest) (*upstream.GitWatcher, error)
		DeleteGitWatcher(ctx context.Context, name string) error
		ListAppBuilds(ctx context.Context, name string, pageSize int) ([]upstream.Build, error)
		GetAppBuild(ctx context.Context, id int64) (*upstream.Build, error)
	}
	Index interface {
		FindArtifact(ctx context.Context, fullName string) (*upstream.Artifact, error)
		ListVersions(ctx context.Context, artifactID int64) ([]upstream.Version, error)
		SetTag(ctx context.Context, artifactID int64, tag, version string) error
		DeleteTag(ctx context.Context, artifactID int64, tag string) error
		DeleteArtifact(ctx context.Context, artifactID int64) error
	}
	Weave interface {
		Get(ctx context.Context, collection, name string) (upstream.WeaveObject, error)
		Create(ctx context.Context, collection string, obj upstream.WeaveObject) (upstream.WeaveObject, error)
		Delete(ctx context.Context, collection, name string) error
		Fire(ctx context.Context, name string) error
	}
)

// Env carries everything a step needs. One Env serves every run of an operator.
type Env struct {
	Cfg    instancecfg.Config
	Forge  Forge
	Index  Index
	Weave  Weave
	Ledger *ledger.Ledger
	Now    func() time.Time // defaults to time.Now
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Input is one step instance's call. Params are already resolved (no placeholders left).
type Input struct {
	Run       string            // WizardRun name
	Key       string            // step instance key: "<name>" or "<name>/<item>"
	Params    map[string]string // resolved step params
	Prior     map[string]string // outputs recorded by earlier polls of this same instance
	StartedAt time.Time         // when this instance first ran; zero on the first call
}

// Result is the outcome of one Ensure call.
type Result struct {
	// Done is true once the step is complete. Otherwise the reconciler calls Ensure again after Requeue.
	Done    bool
	Requeue time.Duration
	// Message explains what a not-yet-done step is waiting for.
	Message string
	// Outputs are stored in run status even while not Done (waitBuild keeps its build ID there).
	Outputs map[string]string
	// Resources are the ledger references the step holds; set when Done.
	Resources []wizardv1.ManagedResourceRef
}

// ParamSpec declares which params a step accepts, so definitions with typos are rejected up front.
type ParamSpec struct {
	Required []string
	Optional []string
	Prefixes []string // any key starting with one of these is accepted (e.g. "override.")
}

func (p ParamSpec) allows(name string) bool {
	for _, n := range p.Required {
		if n == name {
			return true
		}
	}
	for _, n := range p.Optional {
		if n == name {
			return true
		}
	}
	for _, pre := range p.Prefixes {
		if strings.HasPrefix(name, pre) && len(name) > len(pre) {
			return true
		}
	}
	return false
}

// Step is one catalogue entry.
type Step interface {
	Type() wizardv1.StepType
	Params() ParamSpec
	// Outputs lists the output keys a completed step provides for ${steps.<name>.outputs.<key>}.
	Outputs() []string
	// Ensure is idempotent and safe to call repeatedly; see Result for the polling contract.
	Ensure(ctx context.Context, env *Env, in Input) (Result, error)
}

// PermanentError marks a failure retrying cannot fix (bad input, conflict, failed build).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func permanentf(format string, args ...any) error {
	return &PermanentError{Err: fmt.Errorf(format, args...)}
}

// IsPermanent reports whether the reconciler should fail the step instead of retrying. An
// upstream HTTP 4xx (other than 429) is permanent; transport errors, 5xx, and anything
// unclassified are treated as transient.
func IsPermanent(err error) bool {
	var pe *PermanentError
	if errors.As(err, &pe) {
		return true
	}
	var ae *upstream.APIError
	return errors.As(err, &ae) && !upstream.IsTransient(err)
}
