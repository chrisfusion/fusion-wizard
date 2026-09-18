// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const maxEnsureAttempts = 5

// existing describes a resource found upstream.
type existing struct {
	ExternalID string
	// Matches reports whether the resource has the content this step wants. A mismatch is a
	// visible conflict; the wizard never silently reuses or rewrites a resource that differs.
	Matches bool
	// Detail says what differs, for the conflict message.
	Detail string
}

// managed is the recipe for ensuring one upstream resource through the ledger.
type managed struct {
	Key ledger.Key
	// Hash identifies the content this step wants. Two runs asking for the same key with different
	// hashes conflict. Empty disables the check (resources that legitimately move, like tags).
	Hash string
	// Claim registers a resource found without a ledger entry as managed instead of unowned. Used
	// for the index artifact, which forge creates on the wizard's behalf.
	Claim bool
	// Get returns (nil, nil) when the resource does not exist.
	Get func(ctx context.Context) (*existing, error)
	// Create makes the resource and returns its upstream ID (empty when the name is the identity).
	Create func(ctx context.Context) (string, error)
	// Update, if set, runs when the resource exists and is compatible (tags re-point here).
	Update func(ctx context.Context, ex *existing) error
}

// ensure makes the resource exist and records this step's reference in the ledger.
//
// The ledger entry is written BEFORE the resource is created (write-ahead). A crash between the
// two leaves a managed entry for a resource that does not exist, which the next attempt repairs
// and a rollback deletes harmlessly (deletes are idempotent). The reverse order could leak a
// resource no rollback knows about.
//
// It returns ledger.ErrTerminating while a previous owner is still deleting the resource.
func (e *Env) ensure(ctx context.Context, in Input, m managed) (wizardv1.ManagedResourceRef, error) {
	ref := wizardv1.ResourceReference{Run: in.Run, Step: in.Key}
	out := wizardv1.ManagedResourceRef{Service: m.Key.Service, Kind: m.Key.Kind, Name: m.Key.Name}
	what := fmt.Sprintf("%s %s %q", m.Key.Service, m.Key.Kind, m.Key.Name)

	for attempt := 0; attempt < maxEnsureAttempts; attempt++ {
		entry, err := e.Ledger.Get(ctx, m.Key)
		if err != nil {
			return out, err
		}
		if entry != nil && entry.Spec.Terminating {
			return out, ledger.ErrTerminating
		}
		ex, err := m.Get(ctx)
		if err != nil {
			return out, err
		}

		if ex != nil { // the resource exists upstream
			if !ex.Matches {
				return out, permanentf("%s already exists with different settings (%s)", what, ex.Detail)
			}
			if entry != nil && entry.Spec.SpecHash != "" && m.Hash != "" && entry.Spec.SpecHash != m.Hash {
				return out, permanentf("%s is in use by another wizard run with different settings", what)
			}
			out.ExternalID = ex.ExternalID
			if entry == nil {
				_, err = e.Ledger.Register(ctx, m.Key, ref, ledger.NewEntry{Managed: m.Claim, SpecHash: m.Hash, ExternalID: ex.ExternalID})
				if errors.Is(err, ledger.ErrExists) {
					continue // lost the registration race; re-read and adopt
				}
				if err != nil {
					return out, err
				}
				out.Disposition = wizardv1.DispositionFoundUnowned
				if m.Claim {
					out.Disposition = wizardv1.DispositionCreated
				}
			} else {
				updated, err := e.Ledger.AddRef(ctx, m.Key, ref, ledger.AddOptions{})
				if errors.Is(err, ledger.ErrNotFound) {
					continue
				}
				if err != nil {
					return out, err
				}
				out.Disposition = dispositionOf(updated, ref)
			}
			if m.Update != nil {
				if err := m.Update(ctx, ex); err != nil {
					return out, err
				}
			}
			return out, nil
		}

		// The resource does not exist: register first, then create.
		var registered *wizardv1.WizardResource
		if entry == nil {
			registered, err = e.Ledger.Register(ctx, m.Key, ref, ledger.NewEntry{Managed: true, SpecHash: m.Hash})
			if errors.Is(err, ledger.ErrExists) {
				continue
			}
		} else {
			// A stale entry: the resource vanished upstream. Recreating it makes it ours.
			registered, err = e.Ledger.AddRef(ctx, m.Key, ref, ledger.AddOptions{Manage: true})
			if errors.Is(err, ledger.ErrNotFound) {
				continue
			}
		}
		if err != nil {
			return out, err
		}

		id, err := m.Create(ctx)
		if errors.Is(err, upstream.ErrConflict) {
			continue // somebody created it between our Get and Create; next pass adopts it
		}
		if err != nil {
			return out, err // the entry stays; the next attempt or a rollback cleans up
		}
		if id != "" {
			if registered, err = e.Ledger.AddRef(ctx, m.Key, ref, ledger.AddOptions{ExternalID: id}); err != nil {
				return out, err
			}
			out.ExternalID = id
		}
		out.Disposition = dispositionOf(registered, ref)
		return out, nil
	}
	return out, fmt.Errorf("could not settle ownership of %s after %d attempts: concurrent changes", what, maxEnsureAttempts)
}

// dispositionOf is informational only (rollback decides from the ledger's managed flag and
// reference count, never from this): a sole owner of a managed resource created it, other
// owners adopted it, and an unmanaged entry means nobody here created the resource.
func dispositionOf(entry *wizardv1.WizardResource, ref wizardv1.ResourceReference) wizardv1.Disposition {
	switch {
	case !entry.Spec.Managed:
		return wizardv1.DispositionFoundUnowned
	case len(entry.Spec.Refs) == 1 && entry.Spec.Refs[0] == ref:
		return wizardv1.DispositionCreated
	default:
		return wizardv1.DispositionAdopted
	}
}

// ensureStep wraps ensure into a Result, turning "still being deleted by a previous owner" into a
// wait instead of an error.
func (e *Env) ensureStep(ctx context.Context, in Input, m managed, outputs map[string]string) (Result, error) {
	ref, err := e.ensure(ctx, in, m)
	if errors.Is(err, ledger.ErrTerminating) {
		return Result{
			Requeue: retryTerminating,
			Message: fmt.Sprintf("waiting for a previous owner to finish deleting %s %q", m.Key.Kind, m.Key.Name),
			Outputs: in.Prior,
		}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Done: true, Outputs: outputs, Resources: []wizardv1.ManagedResourceRef{ref}}, nil
}

// deleteUpstream deletes from the ledger entry's own record, not from run status: the entry is
// the authoritative description of the resource.
func (e *Env) deleteUpstream(ctx context.Context, s wizardv1.WizardResourceSpec) error {
	switch {
	case s.Service == wizardv1.ServiceForge && s.Kind == KindGitWatcher:
		return e.Forge.DeleteGitWatcher(ctx, s.Name)
	case s.Service == wizardv1.ServiceIndex && s.Kind == KindArtifact:
		id, err := strconv.ParseInt(s.ExternalID, 10, 64)
		if err != nil {
			return permanentf("artifact %q has no valid index ID recorded", s.Name)
		}
		return e.Index.DeleteArtifact(ctx, id)
	case s.Service == wizardv1.ServiceIndex && s.Kind == KindTag:
		id, err := strconv.ParseInt(s.ExternalID, 10, 64)
		i := strings.LastIndex(s.Name, ":")
		if err != nil || i < 0 {
			return permanentf("tag %q has no valid artifact ID recorded", s.Name)
		}
		return e.Index.DeleteTag(ctx, id, s.Name[i+1:])
	case s.Service == wizardv1.ServiceWeave && (s.Kind == KindJobTemplate || s.Kind == KindChain || s.Kind == KindTrigger):
		return e.Weave.Delete(ctx, weaveCollection(s.Kind), s.Name)
	}
	return permanentf("no delete implemented for %s %s", s.Service, s.Kind)
}

func weaveCollection(kind string) string {
	switch kind {
	case KindJobTemplate:
		return upstream.WeaveJobTemplates
	case KindChain:
		return upstream.WeaveChains
	default:
		return upstream.WeaveTriggers
	}
}
