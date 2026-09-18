// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package ledger

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func newLedger(t *testing.T) *Ledger {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := wizardv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return New(fake.NewClientBuilder().WithScheme(s).Build(), "fusion")
}

var (
	tpl  = Key{wizardv1.ServiceWeave, "jobtemplate", "py-runner"}
	refA = wizardv1.ResourceReference{Run: "run-a", Step: "template"}
	refB = wizardv1.ResourceReference{Run: "run-b", Step: "template"}
)

func TestObjectName(t *testing.T) {
	keys := []Key{
		tpl,
		{wizardv1.ServiceIndex, "artifact", "app.My_Tool"},
		{wizardv1.ServiceIndex, "tag", "app.foo:stable"},
		{wizardv1.ServiceForge, "gitwatcher", "x"},
		{wizardv1.ServiceWeave, "trigger", "Ünïcode name!!"},
		{wizardv1.ServiceWeave, "trigger", string(make([]byte, 0)) + fmt.Sprintf("%0200d", 7)},
	}
	seen := map[string]Key{}
	for _, k := range keys {
		n := k.ObjectName()
		if len(n) > 63 || !dnsName.MatchString(n) {
			t.Errorf("%+v -> %q is not a valid <=63 char DNS label", k, n)
		}
		if k.ObjectName() != n {
			t.Errorf("%+v: name is not deterministic", k)
		}
		if other, dup := seen[n]; dup {
			t.Errorf("%+v and %+v collide on %q", k, other, n)
		}
		seen[n] = k
	}
	// Different keys whose sanitised text is identical must still differ (hash suffix).
	a := Key{wizardv1.ServiceWeave, "chain", "a.b"}.ObjectName()
	b := Key{wizardv1.ServiceWeave, "chain", "a-b"}.ObjectName()
	if a == b {
		t.Errorf("a.b and a-b must not collide: %q", a)
	}
}

func TestHash(t *testing.T) {
	if Hash("ab", "c") == Hash("a", "bc") {
		t.Error("part boundaries must matter")
	}
	if Hash("x", "y") != Hash("x", "y") {
		t.Error("Hash must be deterministic")
	}
}

func TestRegisterGetAndRace(t *testing.T) {
	l, ctx := newLedger(t), context.Background()

	if e, err := l.Get(ctx, tpl); e != nil || err != nil {
		t.Fatalf("Get on empty ledger = %v, %v; want nil, nil", e, err)
	}
	e, err := l.Register(ctx, tpl, refA, NewEntry{Managed: true, SpecHash: "h1", ExternalID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Labels[LabelService] != "weave" || e.Labels[LabelKind] != "jobtemplate" {
		t.Errorf("labels = %v", e.Labels)
	}
	got, _ := l.Get(ctx, tpl)
	if got == nil || got.Spec.Name != "py-runner" || !got.Spec.Managed || got.Spec.SpecHash != "h1" || got.Spec.ExternalID != "7" || len(got.Spec.Refs) != 1 {
		t.Fatalf("stored entry = %+v", got)
	}
	if _, err := l.Register(ctx, tpl, refB, NewEntry{Managed: true}); !errors.Is(err, ErrExists) {
		t.Errorf("second Register: want ErrExists, got %v", err)
	}
}

func TestAddRef(t *testing.T) {
	l, ctx := newLedger(t), context.Background()

	if _, err := l.AddRef(ctx, tpl, refA, AddOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddRef on missing entry: want ErrNotFound, got %v", err)
	}
	if _, err := l.Register(ctx, tpl, refA, NewEntry{Managed: false, ExternalID: "1"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second call is a no-op
		if _, err := l.AddRef(ctx, tpl, refB, AddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := l.Get(ctx, tpl)
	if len(e.Spec.Refs) != 2 || e.Spec.Managed {
		t.Fatalf("refs=%v managed=%v", e.Spec.Refs, e.Spec.Managed)
	}

	if _, err := l.AddRef(ctx, tpl, refA, AddOptions{Manage: true, ExternalID: "9"}); err != nil {
		t.Fatal(err)
	}
	e, _ = l.Get(ctx, tpl)
	if !e.Spec.Managed || e.Spec.ExternalID != "9" || len(e.Spec.Refs) != 2 {
		t.Errorf("Manage/ExternalID not applied: %+v", e.Spec)
	}
}

func TestReleaseSharedThenLastManaged(t *testing.T) {
	l, ctx := newLedger(t), context.Background()
	_, _ = l.Register(ctx, tpl, refA, NewEntry{Managed: true})
	_, _ = l.AddRef(ctx, tpl, refB, AddOptions{})

	out, _, err := l.Release(ctx, tpl, refA)
	if err != nil || out != OutcomeShared {
		t.Fatalf("first release = %v, %v; want Shared", out, err)
	}
	if out, _, _ := l.Release(ctx, tpl, refA); out != OutcomeShared {
		t.Errorf("releasing an already-released ref must stay Shared, got %v", out)
	}

	out, e, err := l.Release(ctx, tpl, refB)
	if err != nil || out != OutcomeDelete || e == nil {
		t.Fatalf("last release = %v, %v; want Delete", out, err)
	}
	stored, _ := l.Get(ctx, tpl)
	if stored == nil || !stored.Spec.Terminating || len(stored.Spec.Refs) != 0 {
		t.Fatalf("entry must stay as a terminating tombstone until Finish, got %+v", stored)
	}
	if _, err := l.AddRef(ctx, tpl, refA, AddOptions{}); !errors.Is(err, ErrTerminating) {
		t.Errorf("AddRef on a terminating entry: want ErrTerminating, got %v", err)
	}

	// Crash recovery: repeating the release resumes with Delete, not Gone.
	if out, _, _ := l.Release(ctx, tpl, refB); out != OutcomeDelete {
		t.Errorf("repeated release of a terminating entry = %v, want Delete", out)
	}

	if err := l.Finish(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if e, _ := l.Get(ctx, tpl); e != nil {
		t.Error("Finish must remove the entry")
	}
	if err := l.Finish(ctx, tpl); err != nil {
		t.Errorf("Finish must be idempotent, got %v", err)
	}
	if out, _, _ := l.Release(ctx, tpl, refA); out != OutcomeGone {
		t.Errorf("release after Finish = %v, want Gone", out)
	}
	// After Finish the key is free again: a new run registers a fresh entry.
	if _, err := l.Register(ctx, tpl, refA, NewEntry{Managed: true}); err != nil {
		t.Errorf("re-registering after Finish: %v", err)
	}
}

func TestReleaseUnmanagedNeverAsksForDeletion(t *testing.T) {
	l, ctx := newLedger(t), context.Background()
	_, _ = l.Register(ctx, tpl, refA, NewEntry{Managed: false})
	out, _, err := l.Release(ctx, tpl, refA)
	if err != nil || out != OutcomeReleased {
		t.Fatalf("release = %v, %v; want Released", out, err)
	}
	if e, _ := l.Get(ctx, tpl); e != nil {
		t.Error("the unmanaged entry must be dropped with its last reference")
	}
}

func TestConcurrentAddThenReleaseDeletesExactlyOnce(t *testing.T) {
	l, ctx := newLedger(t), context.Background()
	const runs = 25

	ref := func(i int) wizardv1.ResourceReference {
		return wizardv1.ResourceReference{Run: fmt.Sprintf("run-%d", i), Step: "template"}
	}

	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Same shape as a step's ensure: register, or adopt when we lost the race.
			for {
				_, err := l.Register(ctx, tpl, ref(i), NewEntry{Managed: true})
				if err == nil {
					return
				}
				if !errors.Is(err, ErrExists) {
					t.Errorf("Register: %v", err)
					return
				}
				if _, err = l.AddRef(ctx, tpl, ref(i), AddOptions{}); err == nil {
					return
				}
				if !errors.Is(err, ErrNotFound) {
					t.Errorf("AddRef: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	e, _ := l.Get(ctx, tpl)
	if e == nil || len(e.Spec.Refs) != runs {
		t.Fatalf("want %d refs after concurrent adds, got %+v", runs, e)
	}

	var deletes, shared atomic.Int32
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, _, err := l.Release(ctx, tpl, ref(i))
			if err != nil {
				t.Errorf("Release: %v", err)
				return
			}
			switch out {
			case OutcomeDelete:
				deletes.Add(1)
			case OutcomeShared:
				shared.Add(1)
			default:
				t.Errorf("unexpected outcome %v", out)
			}
		}(i)
	}
	wg.Wait()

	if deletes.Load() != 1 || shared.Load() != runs-1 {
		t.Fatalf("exactly one release may trigger the upstream delete: deletes=%d shared=%d", deletes.Load(), shared.Load())
	}
}
