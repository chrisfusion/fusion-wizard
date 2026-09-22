// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"errors"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// ---- batchTrigger ----

type batchTriggerStep struct{}

func (batchTriggerStep) Type() wizardv1.StepType { return wizardv1.StepBatchTrigger }
func (batchTriggerStep) Outputs() []string       { return []string{"name"} }
func (batchTriggerStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name", "chain", "jobs"}}
}

// Ensure creates a single BatchCron WeaveTrigger through weave's dedicated /batchtriggers endpoint,
// which also provisions the backing jobs ConfigMap — the generic trigger endpoint the "trigger" step
// uses has no way to carry an inline job list. jobs is opaque text (YAML or JSON); weave parses and
// validates it at creation time, so a malformed blob fails this step with weave's own error message
// rather than the field-level pre-validation spectra's UI used to offer.
//
// The dedicated create endpoint has no labels field of its own, so managed-by labels are stamped in
// a second call (PatchLabels) right after a successful create — same "only on real creation, never
// on adopt" guarantee as the trigger step's fireOnCreate, for the same reason: this closure only
// runs from ensure()'s "resource does not exist yet" branch.
func (batchTriggerStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, chain, jobs := in.Params["name"], in.Params["chain"], in.Params["jobs"]

	return env.ensureStep(ctx, in, managed{
		Key:  ledger.Key{Service: wizardv1.ServiceWeave, Kind: KindBatchTrigger, Name: name},
		Hash: ledger.Hash(KindBatchTrigger, chain, jobs),
		Get: func(ctx context.Context) (*existing, error) {
			found, err := env.Weave.Get(ctx, upstream.WeaveTriggers, name)
			if errors.Is(err, upstream.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			spec, _ := found["spec"].(map[string]any)
			diff := ""
			switch {
			case nestedString(spec, "type") != "BatchCron":
				diff = "it is not a BatchCron trigger"
			case nestedString(spec, "chainRef", "name") != chain:
				diff = "it triggers a different chain"
			}
			return &existing{Matches: diff == "", Detail: diff}, nil
		},
		Create: func(ctx context.Context) (string, error) {
			if err := env.Weave.CreateBatchTrigger(ctx, name, chain, jobs); err != nil {
				return "", err
			}
			if err := env.Weave.PatchLabels(ctx, name, map[string]string{
				ledger.LabelManagedBy: ledger.ManagedByWizard,
				ledger.LabelRun:       in.Run,
			}); err != nil {
				return "", err
			}
			return "", nil
		},
	}, map[string]string{"name": name})
}
