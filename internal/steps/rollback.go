// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"fusion-platform.io/fusion-wizard/internal/ledger"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// undoRank is the order rollback deletes resource kinds in (lowest first). It is NOT the reverse of
// the catalogue order: the watcher must go before the index artifact and tag it feeds, or forge
// would rebuild the artifact right after it was removed. Unknown kinds go last.
var undoRank = map[string]int{
	KindTrigger:     10,
	KindChain:       20,
	KindJobTemplate: 30,
	KindGitWatcher:  40,
	KindTag:         50,
	KindArtifact:    60,
}

func rankOf(kind string) int {
	if r, ok := undoRank[kind]; ok {
		return r
	}
	return 100
}

// Rollback releases everything the run holds, working from the ledger and not from run status: a
// step that failed midway (say, after its ledger entry was written but before the upstream create
// returned) never made it into status, yet still owns a reference. A managed resource is deleted
// upstream only when the run held its last reference; shared and unmanaged resources are left in
// place. Rollback is idempotent and reports every failure, so a partial rollback is simply re-run.
func (e *Env) Rollback(ctx context.Context, run string) error {
	var errs []error
	if err := e.SweepTerminating(ctx); err != nil {
		errs = append(errs, err)
	}

	entries, err := e.Ledger.List(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list ledger: %w", err))...)
	}
	var mine []wizardv1.WizardResource
	for _, en := range entries {
		if refsOf(en, run) != nil {
			mine = append(mine, en)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool {
		ri, rj := rankOf(mine[i].Spec.Kind), rankOf(mine[j].Spec.Kind)
		if ri != rj {
			return ri < rj
		}
		return mine[i].Spec.Name < mine[j].Spec.Name
	})

	for _, en := range mine {
		key := ledger.Key{Service: en.Spec.Service, Kind: en.Spec.Kind, Name: en.Spec.Name}
		for _, ref := range refsOf(en, run) {
			if err := e.releaseAndDelete(ctx, key, ref); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// SweepTerminating finishes deletions that a crash interrupted: an entry marked Terminating with no
// references left has nobody responsible for it any more. Rollback runs it first, and the
// operator can also run it periodically.
func (e *Env) SweepTerminating(ctx context.Context) error {
	entries, err := e.Ledger.List(ctx)
	if err != nil {
		return fmt.Errorf("list ledger: %w", err)
	}
	var errs []error
	for _, en := range entries {
		if !en.Spec.Terminating || len(en.Spec.Refs) > 0 {
			continue
		}
		key := ledger.Key{Service: en.Spec.Service, Kind: en.Spec.Kind, Name: en.Spec.Name}
		if en.Spec.Managed {
			if err := e.deleteUpstream(ctx, en.Spec); err != nil {
				errs = append(errs, fmt.Errorf("delete %s %q: %w", en.Spec.Kind, en.Spec.Name, err))
				continue
			}
		}
		if err := e.Ledger.Finish(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("finish %s %q: %w", en.Spec.Kind, en.Spec.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Env) releaseAndDelete(ctx context.Context, key ledger.Key, ref wizardv1.ResourceReference) error {
	outcome, entry, err := e.Ledger.Release(ctx, key, ref)
	if err != nil {
		return fmt.Errorf("release %s %q: %w", key.Kind, key.Name, err)
	}
	if outcome != ledger.OutcomeDelete {
		return nil
	}
	if err := e.deleteUpstream(ctx, entry.Spec); err != nil {
		// The entry stays Terminating so nobody adopts it; the next Rollback or sweep resumes here.
		return fmt.Errorf("delete %s %q: %w", key.Kind, key.Name, err)
	}
	if err := e.Ledger.Finish(ctx, key); err != nil {
		return fmt.Errorf("finish %s %q: %w", key.Kind, key.Name, err)
	}
	return nil
}

func refsOf(en wizardv1.WizardResource, run string) []wizardv1.ResourceReference {
	var refs []wizardv1.ResourceReference
	for _, r := range en.Spec.Refs {
		if r.Run == run {
			refs = append(refs, r)
		}
	}
	return refs
}
