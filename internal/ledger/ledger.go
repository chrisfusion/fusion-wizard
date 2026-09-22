// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package ledger tracks which wizard run steps use which upstream resource, so a rollback deletes
// a shared resource only when the last user is gone. One WizardResource CR per resource is the
// source of truth; every mutation is an optimistic-concurrency update (resourceVersion), which is
// what makes concurrent applies and rollbacks safe without any locking of our own.
//
// Deletion protocol: the run that removes the last reference marks the entry Terminating in the
// same update, deletes the resource upstream, and only then removes the entry (Finish). While an
// entry is Terminating nobody can add a reference (ErrTerminating), so an adopter can never latch
// onto a resource that is halfway through deletion.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	// LabelService and LabelKind make ledger entries listable per upstream service and kind.
	LabelService = "wizard.fusion-platform.io/service"
	LabelKind    = "wizard.fusion-platform.io/kind"

	// LabelManagedBy and LabelRun are stamped on every upstream resource the wizard creates
	// (chains, jobTemplates, triggers, GitWatchers — not the ledger entries above), so a bare
	// `kubectl get -o yaml` shows ownership without querying the wizard's API. LabelManagedBy is a
	// platform-wide, open-valued convention ("wizard" here; other components may use their own
	// value, e.g. a future "manual" default); LabelRun stays scoped under wizard.fusion-platform.io/
	// because weave already uses the unscoped "fusion-platform.io/run" for a different concept (a
	// chain execution, not a WizardRun).
	LabelManagedBy  = "fusion-platform.io/managed-by"
	ManagedByWizard = "wizard"
	LabelRun        = "wizard.fusion-platform.io/run"

	maxBaseLen = 54 // 54 + "-" + 8 hex = 63, so the name is also usable as a label value
)

// conflictBackoff is deliberately more patient than retry.DefaultRetry (5 quick attempts): sharing
// a resource means many runs update the same entry at once, and every lost race must be retried
// rather than surfaced as a failed step.
var conflictBackoff = wait.Backoff{
	Steps:    30,
	Duration: 5 * time.Millisecond,
	Factor:   1.5,
	Jitter:   0.5,
	Cap:      200 * time.Millisecond,
}

var (
	// ErrTerminating: the entry is being deleted; retry once it is gone.
	ErrTerminating = errors.New("ledger entry is being deleted")
	// ErrNotFound: no entry exists for the key.
	ErrNotFound = errors.New("ledger entry not found")
	// ErrExists: an entry already exists for the key (lost a creation race).
	ErrExists = errors.New("ledger entry already exists")
)

// Key identifies one upstream resource.
type Key struct {
	Service wizardv1.ServiceName
	Kind    string
	Name    string
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// ObjectName is the deterministic, DNS-safe name of the key's ledger entry: a readable prefix plus
// a hash of the exact key, so two keys never collide even when their sanitised forms match.
func (k Key) ObjectName() string {
	sum := sha256.Sum256([]byte(string(k.Service) + "/" + k.Kind + "/" + k.Name))
	base := strings.Trim(nonName.ReplaceAllString(strings.ToLower(string(k.Service)+"-"+k.Kind+"-"+k.Name), "-"), "-")
	if len(base) > maxBaseLen {
		base = strings.TrimRight(base[:maxBaseLen], "-")
	}
	return base + "-" + hex.EncodeToString(sum[:4])
}

// Hash builds a spec hash from the identity-defining parts of a resource. Parts are joined with a
// separator that cannot occur in them, so ("ab","c") and ("a","bc") differ.
func Hash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Outcome tells the caller of Release what is left to do.
type Outcome int

const (
	// OutcomeGone: there was no entry; nothing to do.
	OutcomeGone Outcome = iota
	// OutcomeShared: other runs still reference the resource; leave it alone.
	OutcomeShared
	// OutcomeReleased: the last reference of an unmanaged resource was dropped and its entry
	// removed; the resource itself is never deleted.
	OutcomeReleased
	// OutcomeDelete: the last reference of a managed resource was dropped. The caller must delete
	// the resource upstream and then call Finish.
	OutcomeDelete
)

// Ledger reads and writes WizardResource entries in one namespace.
type Ledger struct {
	c  client.Client
	ns string
}

func New(c client.Client, namespace string) *Ledger { return &Ledger{c: c, ns: namespace} }

func (l *Ledger) nn(k Key) types.NamespacedName {
	return types.NamespacedName{Namespace: l.ns, Name: k.ObjectName()}
}

// Get returns the entry, or (nil, nil) when there is none.
func (l *Ledger) Get(ctx context.Context, k Key) (*wizardv1.WizardResource, error) {
	var e wizardv1.WizardResource
	if err := l.c.Get(ctx, l.nn(k), &e); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// NewEntry are the fields of a freshly registered entry.
type NewEntry struct {
	Managed    bool
	SpecHash   string
	ExternalID string
}

// Register creates the entry with ref as its first reference. It returns ErrExists when another
// run registered the same key first; the caller then re-reads the entry and adopts it.
func (l *Ledger) Register(ctx context.Context, k Key, ref wizardv1.ResourceReference, n NewEntry) (*wizardv1.WizardResource, error) {
	e := &wizardv1.WizardResource{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: l.ns,
			Name:      k.ObjectName(),
			Labels:    map[string]string{LabelService: string(k.Service), LabelKind: k.Kind},
		},
		Spec: wizardv1.WizardResourceSpec{
			Service:    k.Service,
			Kind:       k.Kind,
			Name:       k.Name,
			ExternalID: n.ExternalID,
			SpecHash:   n.SpecHash,
			Managed:    n.Managed,
			Refs:       []wizardv1.ResourceReference{ref},
		},
	}
	if err := l.c.Create(ctx, e); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, ErrExists
		}
		return nil, err
	}
	return e, nil
}

// AddOptions adjusts an entry while adding a reference.
type AddOptions struct {
	// Manage flips the entry to managed. Used when the wizard recreates a resource that vanished
	// upstream: it is the wizard's own creation now, whoever made the original.
	Manage bool
	// ExternalID, when set, replaces the recorded upstream ID (recreated resources get new IDs).
	ExternalID string
}

// AddRef adds ref to an existing entry; adding an existing ref is a no-op. It returns ErrNotFound
// if the entry disappeared and ErrTerminating if it is being deleted.
func (l *Ledger) AddRef(ctx context.Context, k Key, ref wizardv1.ResourceReference, opts AddOptions) (*wizardv1.WizardResource, error) {
	var out *wizardv1.WizardResource
	err := retry.RetryOnConflict(conflictBackoff, func() error {
		e, err := l.Get(ctx, k)
		if err != nil {
			return err
		}
		if e == nil {
			return ErrNotFound
		}
		if e.Spec.Terminating {
			return ErrTerminating
		}
		changed := false
		if !hasRef(e.Spec.Refs, ref) {
			e.Spec.Refs = append(e.Spec.Refs, ref)
			changed = true
		}
		if opts.Manage && !e.Spec.Managed {
			e.Spec.Managed = true
			changed = true
		}
		if opts.ExternalID != "" && e.Spec.ExternalID != opts.ExternalID {
			e.Spec.ExternalID = opts.ExternalID
			changed = true
		}
		if changed {
			if err := l.c.Update(ctx, e); err != nil {
				return err
			}
		}
		out = e
		return nil
	})
	return out, err
}

// Release drops ref from the entry and reports what the caller still has to do. It is idempotent:
// calling it again after a crash resumes where the previous call stopped (an entry already marked
// Terminating with no references yields OutcomeDelete again).
func (l *Ledger) Release(ctx context.Context, k Key, ref wizardv1.ResourceReference) (Outcome, *wizardv1.WizardResource, error) {
	var (
		outcome Outcome
		entry   *wizardv1.WizardResource
	)
	err := retry.RetryOnConflict(conflictBackoff, func() error {
		e, err := l.Get(ctx, k)
		if err != nil {
			return err
		}
		if e == nil {
			outcome, entry = OutcomeGone, nil
			return nil
		}
		changed := false
		if i := indexOfRef(e.Spec.Refs, ref); i >= 0 {
			e.Spec.Refs = append(e.Spec.Refs[:i], e.Spec.Refs[i+1:]...)
			changed = true
		}
		if len(e.Spec.Refs) > 0 {
			if changed {
				if err := l.c.Update(ctx, e); err != nil {
					return err
				}
			}
			outcome, entry = OutcomeShared, e
			return nil
		}
		// Last reference gone: mark terminating so nobody can adopt it from here on.
		if !e.Spec.Terminating {
			e.Spec.Terminating = true
			changed = true
		}
		if changed {
			if err := l.c.Update(ctx, e); err != nil {
				return err
			}
		}
		if e.Spec.Managed {
			outcome, entry = OutcomeDelete, e
			return nil
		}
		if err := l.c.Delete(ctx, e); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		outcome, entry = OutcomeReleased, e
		return nil
	})
	return outcome, entry, err
}

// Finish removes a Terminating entry after its resource was deleted upstream.
func (l *Ledger) Finish(ctx context.Context, k Key) error {
	e, err := l.Get(ctx, k)
	if err != nil || e == nil {
		return err
	}
	if err := l.c.Delete(ctx, e); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// List returns every entry in the namespace (used by the read-only API and tests).
func (l *Ledger) List(ctx context.Context) ([]wizardv1.WizardResource, error) {
	var list wizardv1.WizardResourceList
	if err := l.c.List(ctx, &list, client.InNamespace(l.ns)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func hasRef(refs []wizardv1.ResourceReference, ref wizardv1.ResourceReference) bool {
	return indexOfRef(refs, ref) >= 0
}

func indexOfRef(refs []wizardv1.ResourceReference, ref wizardv1.ResourceReference) int {
	for i, r := range refs {
		if r == ref {
			return i
		}
	}
	return -1
}
