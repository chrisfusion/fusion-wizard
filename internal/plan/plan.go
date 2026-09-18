// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package plan expands a wizard definition into the ordered step instances a run executes. It is
// shared by the reconciler (which runs the plan) and the API server (which rejects an
// unexecutable plan at creation time instead of letting the run fail later).
package plan

import (
	"fmt"

	"fusion-platform.io/fusion-wizard/internal/params"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// Instance is one executable step: a definition step, or one forEach expansion of it.
type Instance struct {
	Key  string // the step name, or "<name>/<item>" for a forEach expansion
	Step wizardv1.WizardStep
	Item *string // set for forEach expansions
}

// Expand turns a definition into its ordered step instances. Parameters are known when the run
// starts, so every forEach is expanded up front and the whole plan shows in status as Pending.
// Items must be non-empty and unique, since they become part of the instance key.
func Expand(def *wizardv1.WizardDefinitionSpec, values map[string]any) ([]Instance, error) {
	ctx := params.Context{Params: values}
	seen := map[string]struct{}{}
	var out []Instance

	add := func(in Instance) error {
		if _, dup := seen[in.Key]; dup {
			return fmt.Errorf("step instance %q occurs twice (forEach items must be unique)", in.Key)
		}
		seen[in.Key] = struct{}{}
		out = append(out, in)
		return nil
	}

	for _, st := range def.Steps {
		if st.ForEach == "" {
			if err := add(Instance{Key: st.Name, Step: st}); err != nil {
				return nil, err
			}
			continue
		}
		items, err := params.ResolveList(st.ForEach, ctx)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
		for _, item := range items {
			if item == "" {
				return nil, fmt.Errorf("step %q: forEach items must not be empty", st.Name)
			}
			item := item
			if err := add(Instance{Key: st.Name + "/" + item, Step: st, Item: &item}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
