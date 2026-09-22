// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"fusion-platform.io/fusion-wizard/internal/params"
	"fusion-platform.io/fusion-wizard/internal/plan"
	"fusion-platform.io/fusion-wizard/internal/steps"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const (
	// maxStepFailures is how many consecutive transient failures a step survives.
	maxStepFailures = 10
	// rollbackRetryInterval: rollback is idempotent, so a failed one is simply retried.
	rollbackRetryInterval = 30 * time.Second
	// envRetryInterval: the instance config (ConfigMap) is missing or invalid.
	envRetryInterval = 30 * time.Second
	defaultPoll      = 5 * time.Second
	maxRetryDelay    = time.Minute
)

// WizardRunReconciler drives WizardRuns through the step catalogue and rolls them back.
type WizardRunReconciler struct {
	client.Client
	Envs EnvFactory
	Now  func() time.Time // defaults to time.Now
}

func (r *WizardRunReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

// runChanged filters WizardRun update events. Status writes must not retrigger the reconciler (each
// reconcile would poll upstream once more), so only spec, annotation and deletion changes count;
// polling is driven by RequeueAfter.
func runChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectNew.GetGeneration() != e.ObjectOld.GetGeneration() ||
			!reflect.DeepEqual(e.ObjectNew.GetAnnotations(), e.ObjectOld.GetAnnotations()) ||
			!e.ObjectNew.GetDeletionTimestamp().Equal(e.ObjectOld.GetDeletionTimestamp())
	}}
}

// SetupWithManager registers the reconciler.
func (r *WizardRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wizardv1.WizardRun{}, builder.WithPredicates(runChanged())).
		Complete(r)
}

func (r *WizardRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run wizardv1.WizardRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !run.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &run)
	}

	// The finalizer goes on before anything is provisioned, so deleting a run always rolls it back.
	if !controllerutil.ContainsFinalizer(&run, wizardv1.FinalizerRollback) {
		patch := client.MergeFrom(run.DeepCopy())
		controllerutil.AddFinalizer(&run, wizardv1.FinalizerRollback)
		if err := r.Patch(ctx, &run, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	if run.Spec.DesiredState == wizardv1.DesiredRolledBack {
		if run.Status.Phase == wizardv1.RunRolledBack {
			return ctrl.Result{}, nil
		}
		res, _, err := r.rollback(ctx, &run)
		return res, err
	}
	return r.reconcileApply(ctx, &run)
}

// ---- apply ----

func (r *WizardRunReconciler) reconcileApply(ctx context.Context, run *wizardv1.WizardRun) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	switch run.Status.Phase {
	case wizardv1.RunRolledBack, wizardv1.RunRollingBack, wizardv1.RunRollbackFailed:
		// A rolled-back run is finished; provisioning again means creating a new run.
		return ctrl.Result{}, r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
			st.Message = "this run was rolled back; create a new run to provision again"
		})
	}

	if run.Spec.DefinitionSnapshot == nil {
		msg, err := r.takeSnapshot(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if msg != "" {
			return ctrl.Result{}, r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) { r.setRun(st, wizardv1.RunFailed, msg, run.Generation) })
		}
	}
	def := run.Spec.DefinitionSnapshot

	values, err := params.ResolveParameters(def.Parameters, run.Spec.Parameters)
	if err != nil {
		return ctrl.Result{}, r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
			r.setRun(st, wizardv1.RunFailed, "invalid parameters: "+err.Error(), run.Generation)
		})
	}
	insts, err := plan.Expand(def, values)
	if err != nil {
		return ctrl.Result{}, r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
			r.setRun(st, wizardv1.RunFailed, err.Error(), run.Generation)
		})
	}

	// A retry request resets failed steps; the annotation is removed only after that reset was
	// written, so a crash in between cannot lose the request.
	_, retry := run.Annotations[wizardv1.AnnotationRetry]

	base := client.MergeFrom(run.DeepCopy())
	before := run.Status.DeepCopy()
	result := r.advance(ctx, run, insts, values, retry)
	if !equality.Semantic.DeepEqual(before, &run.Status) {
		if before.Phase != run.Status.Phase {
			log.Info("run phase changed", "from", before.Phase, "to", run.Status.Phase)
		}
		if err := r.Status().Patch(ctx, run, base); err != nil {
			return ctrl.Result{}, err
		}
	}
	if retry {
		patch := client.MergeFrom(run.DeepCopy())
		delete(run.Annotations, wizardv1.AnnotationRetry)
		if err := r.Patch(ctx, run, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// takeSnapshot copies the definition into the run. It returns a failure message for problems no
// retry fixes (missing or invalid definition) and an error for transient ones.
func (r *WizardRunReconciler) takeSnapshot(ctx context.Context, run *wizardv1.WizardRun) (string, error) {
	var def wizardv1.WizardDefinition
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.DefinitionRef.Name}
	if err := r.Get(ctx, key, &def); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("definition %q not found", key.Name), nil
		}
		return "", err
	}
	if err := steps.ValidateDefinition(&def.Spec); err != nil {
		return fmt.Sprintf("definition %q is invalid: %v", key.Name, err), nil
	}
	patch := client.MergeFrom(run.DeepCopy())
	run.Spec.DefinitionSnapshot = def.Spec.DeepCopy()
	run.Spec.DefinitionGeneration = def.Generation
	return "", r.Patch(ctx, run, patch)
}

type outcome int

const (
	outcomeDone outcome = iota
	outcomeWait
	outcomeFailed
)

// advance runs step instances in order for as long as they complete, mutating run.Status only.
// It stops at the first instance that must wait or has failed: later steps depend on earlier ones.
func (r *WizardRunReconciler) advance(ctx context.Context, run *wizardv1.WizardRun, insts []plan.Instance,
	values map[string]any, retry bool) ctrl.Result {

	st := &run.Status
	gen := run.Generation
	st.ObservedGeneration = gen
	syncSteps(st, insts)
	if retry {
		resetFailed(st)
	}
	if st.StartedAt == nil {
		now := r.now()
		st.StartedAt = &now
	}

	var env *steps.Env
	outputs := map[string]map[string]string{} // outputs of completed non-forEach steps, by step name
	for _, in := range insts {
		s := findStep(st, in.Key)
		switch s.Phase {
		case wizardv1.StepSucceeded:
			if in.Item == nil {
				outputs[in.Step.Name] = s.Outputs
			}
			continue
		case wizardv1.StepFailed:
			r.setRun(st, wizardv1.RunFailed, fmt.Sprintf("step %q failed: %s", in.Key, s.Message), gen)
			return ctrl.Result{}
		}

		if env == nil {
			e, err := r.Envs.Env(ctx)
			if err != nil {
				r.setRun(st, wizardv1.RunRunning, "waiting for a valid instance config: "+err.Error(), gen)
				return ctrl.Result{RequeueAfter: envRetryInterval}
			}
			env = e
		}

		result, wait := r.runInstance(ctx, env, run, in, s, values, outputs)
		switch result {
		case outcomeFailed:
			r.setRun(st, wizardv1.RunFailed, fmt.Sprintf("step %q failed: %s", in.Key, s.Message), gen)
			return ctrl.Result{}
		case outcomeWait:
			msg := ""
			if s.Message != "" {
				msg = fmt.Sprintf("step %q: %s", in.Key, s.Message)
			}
			r.setRun(st, wizardv1.RunRunning, msg, gen)
			return ctrl.Result{RequeueAfter: wait}
		}
		if in.Item == nil {
			outputs[in.Step.Name] = s.Outputs
		}
	}

	r.setRun(st, wizardv1.RunReady, "", gen)
	return ctrl.Result{}
}

// runInstance executes one step instance and records the result in s.
func (r *WizardRunReconciler) runInstance(ctx context.Context, env *steps.Env, run *wizardv1.WizardRun, in plan.Instance,
	s *wizardv1.WizardRunStepStatus, values map[string]any, outputs map[string]map[string]string) (outcome, time.Duration) {

	log := ctrl.LoggerFrom(ctx).WithValues("step", in.Key, "type", in.Step.Type)
	now := r.now()
	fail := func(msg string) (outcome, time.Duration) {
		s.Phase, s.Message, s.Failures, s.CompletedAt = wizardv1.StepFailed, msg, 0, &now
		return outcomeFailed, 0
	}

	step, ok := steps.Lookup(in.Step.Type)
	if !ok {
		return fail(fmt.Sprintf("unknown step type %q", in.Step.Type))
	}
	pctx := params.Context{Params: values, Config: env.Cfg.Values(), Steps: outputs, Item: in.Item, ItemFields: in.ItemFields}
	resolved, err := params.ResolveMap(in.Step.Params, pctx)
	if err != nil {
		return fail("resolving params: " + err.Error())
	}

	if s.StartedAt == nil {
		s.StartedAt = &now
	}
	s.Phase, s.Type = wizardv1.StepRunning, in.Step.Type

	res, err := step.Ensure(ctx, env, steps.Input{
		Run: run.Name, Key: in.Key, Params: resolved, Prior: s.Outputs, StartedAt: s.StartedAt.Time,
	})
	switch {
	case err != nil && steps.IsPermanent(err):
		logUpstreamDetail(log, err, "step failed permanently")
		return fail(err.Error())
	case err != nil:
		s.Failures++
		logUpstreamDetail(log, err, "step failed, will retry", "failures", s.Failures)
		if s.Failures >= maxStepFailures {
			return fail(fmt.Sprintf("giving up after %d attempts: %v", s.Failures, err))
		}
		s.Message = fmt.Sprintf("attempt %d failed, will retry: %v", s.Failures, err)
		return outcomeWait, retryDelay(s.Failures)
	case res.Done:
		s.Phase, s.Message, s.Failures = wizardv1.StepSucceeded, "", 0
		s.Outputs, s.Resources, s.CompletedAt = res.Outputs, res.Resources, &now
		return outcomeDone, 0
	default:
		s.Message, s.Failures = res.Message, 0
		if res.Outputs != nil {
			s.Outputs = res.Outputs // progress the step wants back on its next call (waitBuild's build ID)
		}
		wait := res.Requeue
		if wait <= 0 {
			wait = defaultPoll
		}
		return outcomeWait, wait
	}
}

// retryDelay is an exponential backoff for transient step failures: 2s, 4s, 8s ... capped at 1m.
func retryDelay(failures int32) time.Duration {
	d := 2 * time.Second
	for i := int32(1); i < failures && d < maxRetryDelay; i++ {
		d *= 2
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

func logUpstreamDetail(log interface{ Error(error, string, ...any) }, err error, msg string, kv ...any) {
	var ae *upstream.APIError
	if errors.As(err, &ae) {
		// Status carries the sanitised message; the log keeps the underlying cause.
		log.Error(errors.New(ae.Detail()), msg, kv...)
		return
	}
	log.Error(err, msg, kv...)
}

// ---- rollback and deletion ----

func (r *WizardRunReconciler) reconcileDelete(ctx context.Context, run *wizardv1.WizardRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, wizardv1.FinalizerRollback) {
		return ctrl.Result{}, nil
	}
	if run.Status.Phase != wizardv1.RunRolledBack {
		res, done, err := r.rollback(ctx, run)
		if !done || err != nil {
			return res, err
		}
	}
	patch := client.MergeFrom(run.DeepCopy())
	controllerutil.RemoveFinalizer(run, wizardv1.FinalizerRollback)
	return ctrl.Result{}, r.Patch(ctx, run, patch)
}

// rollback releases everything the run holds (see steps.Env.Rollback). Failure is not terminal:
// rollback is idempotent, so it is retried until it succeeds. done reports success.
func (r *WizardRunReconciler) rollback(ctx context.Context, run *wizardv1.WizardRun) (res ctrl.Result, done bool, err error) {
	gen := run.Generation
	if run.Status.Phase != wizardv1.RunRollingBack && run.Status.Phase != wizardv1.RunRollbackFailed {
		if err := r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
			r.setRun(st, wizardv1.RunRollingBack, "", gen)
		}); err != nil {
			return ctrl.Result{}, false, err
		}
	}

	env, envErr := r.Envs.Env(ctx)
	if envErr == nil {
		envErr = env.Rollback(ctx, run.Name)
	}
	if envErr != nil {
		ctrl.LoggerFrom(ctx).Error(envErr, "rollback failed; will retry")
		err := r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
			r.setRun(st, wizardv1.RunRollbackFailed, "rollback failed, will retry: "+envErr.Error(), gen)
		})
		return ctrl.Result{RequeueAfter: rollbackRetryInterval}, false, err
	}

	now := r.now()
	return ctrl.Result{}, true, r.updateStatus(ctx, run, func(st *wizardv1.WizardRunStatus) {
		for i := range st.Steps {
			if st.Steps[i].Phase != wizardv1.StepPending {
				st.Steps[i].Phase, st.Steps[i].Message, st.Steps[i].CompletedAt = wizardv1.StepRolledBack, "", &now
			}
		}
		r.setRun(st, wizardv1.RunRolledBack, "", gen)
	})
}

// ---- status helpers ----

// updateStatus applies mutate and patches the status subresource if anything changed. The patch
// base is captured right before the mutation, so the diff contains exactly this change.
func (r *WizardRunReconciler) updateStatus(ctx context.Context, run *wizardv1.WizardRun, mutate func(*wizardv1.WizardRunStatus)) error {
	base := client.MergeFrom(run.DeepCopy())
	before := run.Status.DeepCopy()
	mutate(&run.Status)
	if equality.Semantic.DeepEqual(before, &run.Status) {
		return nil
	}
	return r.Status().Patch(ctx, run, base)
}

// setRun sets the run phase, message and Ready condition. Terminal phases record CompletedAt once;
// a run that leaves a terminal phase (retry) clears it.
func (r *WizardRunReconciler) setRun(st *wizardv1.WizardRunStatus, phase wizardv1.RunPhase, msg string, gen int64) {
	st.Phase, st.Message, st.ObservedGeneration = phase, msg, gen

	switch phase {
	case wizardv1.RunReady, wizardv1.RunFailed, wizardv1.RunRolledBack, wizardv1.RunRollbackFailed:
		if st.CompletedAt == nil {
			now := r.now()
			st.CompletedAt = &now
		}
	default:
		st.CompletedAt = nil
	}

	ready := metav1.ConditionFalse
	if phase == wizardv1.RunReady {
		ready = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: wizardv1.ConditionReady, Status: ready, Reason: string(phase), Message: msg, ObservedGeneration: gen,
	})
}

// syncSteps makes status.steps list exactly the planned instances, in order, keeping the progress
// already recorded. Rollback works from the ledger, so dropping an entry never loses ownership.
func syncSteps(st *wizardv1.WizardRunStatus, insts []plan.Instance) {
	known := make(map[string]wizardv1.WizardRunStepStatus, len(st.Steps))
	for _, s := range st.Steps {
		known[s.Key] = s
	}
	out := make([]wizardv1.WizardRunStepStatus, 0, len(insts))
	for _, in := range insts {
		s, ok := known[in.Key]
		if !ok {
			s = wizardv1.WizardRunStepStatus{Key: in.Key, Name: in.Step.Name, Type: in.Step.Type, Phase: wizardv1.StepPending}
			if in.Item != nil {
				s.Item = *in.Item
			}
		}
		out = append(out, s)
	}
	st.Steps = out
}

func findStep(st *wizardv1.WizardRunStatus, key string) *wizardv1.WizardRunStepStatus {
	for i := range st.Steps {
		if st.Steps[i].Key == key {
			return &st.Steps[i]
		}
	}
	return nil // unreachable: syncSteps created an entry for every instance
}

// resetFailed puts failed steps back to Pending for a retry; succeeded steps keep their results.
func resetFailed(st *wizardv1.WizardRunStatus) {
	for i := range st.Steps {
		if st.Steps[i].Phase == wizardv1.StepFailed {
			s := &st.Steps[i]
			s.Phase, s.Message, s.Failures, s.StartedAt, s.CompletedAt = wizardv1.StepPending, "", 0, nil, nil
		}
	}
}
