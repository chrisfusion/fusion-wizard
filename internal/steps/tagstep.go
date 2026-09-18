// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"fmt"
	"strconv"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

type tagStep struct{}

func (tagStep) Type() wizardv1.StepType { return wizardv1.StepTag }
func (tagStep) Outputs() []string       { return []string{"tag", "version"} }
func (tagStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"artifactId", "artifactName", "version"}, Optional: []string{"tag"}}
}

// Ensure points a tag (default: the instance's tagName, "stable") at the built version. The tag
// is a mutable alias, so an existing one is re-pointed instead of treated as a conflict, and it
// has no spec hash. A tag that already existed with no ledger entry is recorded as unmanaged:
// it is moved, but a rollback will not delete it.
func (tagStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	artifactName, version := in.Params["artifactName"], in.Params["version"]
	tag := firstNonEmpty(in.Params["tag"], env.Cfg.TagName)
	artifactID, err := strconv.ParseInt(in.Params["artifactId"], 10, 64)
	if err != nil {
		return Result{}, permanentf("artifactId %q is not a number", in.Params["artifactId"])
	}
	idStr := strconv.FormatInt(artifactID, 10)

	return env.ensureStep(ctx, in, managed{
		Key: ledger.Key{Service: wizardv1.ServiceIndex, Kind: KindTag, Name: fmt.Sprintf("%s:%s", artifactName, tag)},
		Get: func(ctx context.Context) (*existing, error) {
			versions, err := env.Index.ListVersions(ctx, artifactID)
			if err != nil {
				return nil, err
			}
			for _, v := range versions {
				for _, t := range v.Tags {
					if t.Tag == tag {
						return &existing{ExternalID: idStr, Matches: true}, nil
					}
				}
			}
			return nil, nil
		},
		Create: func(ctx context.Context) (string, error) {
			return idStr, env.Index.SetTag(ctx, artifactID, tag, version)
		},
		Update: func(ctx context.Context, _ *existing) error {
			return env.Index.SetTag(ctx, artifactID, tag, version) // re-point to the built version
		},
	}, map[string]string{"tag": tag, "version": version})
}
