# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Added
- Project scaffold: Go module, Makefile, Dockerfile, GPL-3.0 license, `.gitignore`, `CLAUDE.md`
- CRDs in `wizard.fusion-platform.io/v1alpha1`: `WizardDefinition`, `WizardRun`, `WizardResource` (ledger) with generated deepcopy code and manifests
- `internal/params`: `${params.x}` / `${config.x}` / `${steps.s.outputs.k}` / `${item}` templates with `k8sName`, `stem`, `lower` filters, and typed parameter resolution (defaults, required, patterns)
- `internal/instancecfg`: typed per-instance config parsed from a ConfigMap (runner image, resources, tag name, artifact prefix, polling, upstream URLs)
- `internal/upstream`: forge, index and weave REST clients with per-request service-account token re-read and sanitised errors
- `internal/ledger`: ref-counted `WizardResource` ledger with optimistic-concurrency updates and a `Terminating` tombstone protocol (`Register`, `AddRef`, `Release`, `Finish`); own conflict backoff sized for many runs sharing one entry
- `internal/steps`: step catalogue (`gitWatcher`, `waitBuild`, `tag`, `jobTemplate`, `chain`, `trigger`) with idempotent write-ahead `Ensure`, visible conflicts for same-name-different-settings, unowned resources never deleted, and `ValidateDefinition` (catalogue order, unknown params, references to earlier steps only)
- `Env.Rollback(run)` releases every ledger entry a run references in kind order (trigger, chain, template, watcher, tag, artifact) and `Env.SweepTerminating` finishes deletions a crash interrupted
- `upstream.Build.ProjectDir` so `waitBuild` can match a build to its watcher by repository and subfolder
- `internal/controller`: `WizardRun` reconciler (finalizer, definition snapshot, ordered step execution with `forEach` expansion, non-blocking polling, exponential backoff for transient failures with a give-up limit, one-shot `wizard.fusion-platform.io/retry` annotation, rollback via `desiredState: RolledBack` or deletion, retried until it succeeds) and a leader-only `Sweeper` for interrupted ledger deletions
- `cmd/main.go`: operator entrypoint (namespaced cache, leader election, health probes, one projected token source per upstream)
- `internal/steps/stepstest`: shared in-memory fakes of forge, index and weave with an ordered event log
- `WizardRunStepStatus.failures` counter; `FinalizerRollback` and `AnnotationRetry` constants

### Changed
- `ValidateDefinition` rejects references to the outputs of a `forEach` step (several instances make them ambiguous)
- `internal/apiserver` and `cmd/api`: REST API (`/api/v1`) with definitions (parameter schema, validity flag), runs (create with up-front validation of definition, parameters and forEach expansion; get, list with filters, retry, rollback, delete, bulk rollback with a required selector and a target cap) and a read-only ledger view; `{"error", "details"}` error shape; `slog` logging with request IDs
- API authentication: Kubernetes TokenReview, mandatory `AUTH_ALLOWED_SA` allowlist (startup refuses an open API unless `ALLOW_UNAUTHENTICATED=true`), optional audience; `X-User-ID` / `X-User-Email` are read only from allowlisted callers, validated, and stamped on the run
- `internal/plan` (definition expansion shared by the API and the reconciler) and `internal/envutil` (env-driven flag defaults shared by both binaries)
- `stepstest.PythonJob`: one shared reference definition for the steps, controller and API tests
- Helm chart `deployment/fusion-wizard` (v0.1.0): operator and API deployments, RBAC scoped to the chart, instance ConfigMap, preseeded `python-git-job` `WizardDefinition`, and synced `crds/`; `internal/chart` contract-tests the rendered manifests against the code (mutation-checked)

### Added
- Every chain, jobTemplate and trigger the wizard creates in weave is stamped with `fusion-platform.io/managed-by: wizard` and `wizard.fusion-platform.io/run: <run-name>`, so a bare `kubectl get -o yaml` shows ownership without querying the wizard's own ledger API. `managed-by` is a platform-wide, open-valued label (other components may use their own value); `run` stays scoped under `wizard.fusion-platform.io/` because weave already uses the unscoped `fusion-platform.io/run` for a different concept (a chain execution). `internal/upstream.CreateGitWatcherRequest` gained a `Labels` field to carry the same pair to forge; forge's `POST /api/v1/gitwatchers` accepts it as of the same date.
- `internal/apiserver/auth.go`: `Authenticate` now logs a warning with the real Kubernetes `TokenReview` rejection reason (and the requested audiences) instead of only returning a bare 401 — found while diagnosing the `AUTH_AUDIENCE` gotcha below; a rejected token previously left no server-side trace at all.

### Fixed
- `python-git-job`'s `gitWatcher` step never carried a `projectDir` param, unlike `batch-git-job`/`batchcron-git-job` — the definition just never wired it up, since the day it was first written. New `projectDir` parameter (default `""`, matching the other two), wired into the watcher step.
- `waitBuild` no longer fails a run permanently when forge reports a build `SUCCESS` but its index artifact is missing (e.g. deleted by our own rollback): forge's `GitWatcher` reconciler now self-heals this case on its own (see fusion-forge's `index-drift-cleanup`), just not synchronously with our poll, so `waitBuild` retries up to `BuildTimeout` instead of giving up immediately. Verified end-to-end against a real forge/index in minikube.

### Added
- New `objectList` parameter type: a JSON array of flat string-keyed objects, usable as a `forEach` source alongside `stringList`. Every entry needs a non-empty `"key"` field (bare `${item}` still resolves to it, so existing `${item|stem|k8sName}`-style patterns are unaffected); other fields read with the new `${item.<field>}` placeholder syntax (`internal/params/template.go`: `Context.ItemFields`, `ResolveList` now returns `[]ForEachItem{Key, Fields}`; `internal/plan.Instance` carries `ItemFields`; threaded through the reconciler). `python-git-job`'s `entrypoints` parameter is now `objectList` (`key`/`type`/`schedule` per entry) and its trigger step's `type`/`schedule` params reference `${item.type}`/`${item.schedule}` — each entrypoint can independently be OnDemand or Cron with its own schedule, which a plain stringList forEach could never express (every expansion would get the identical value). New `internal/controller.TestMixedOnDemandAndCronEntrypoints` proves it end to end. CRDs regenerated (`ParameterType` enum gained `objectList`).
- New `batchTrigger` step (`WizardStep.Type: batchTrigger`, catalogue position after `trigger`): creates a single BatchCron `WeaveTrigger` (many cron-scheduled job entries, opaque `jobs` text) through weave's dedicated `/batchtriggers` endpoint, since the generic trigger endpoint can't carry an inline job list. Stamps managed-by labels via a follow-up `PatchLabels` call (the dedicated create endpoint has none of its own), only on the call that actually creates the trigger. New `WeaveClient.CreateBatchTrigger`/`DeleteBatchTrigger`/`PatchLabels`; new pre-seeded `WizardDefinition` `batchcron-git-job` (`definitions.batchCronGitJob.enabled`, default `true`) — spectra's "Git BatchCron Job" wizard in catalogue form, matching reference fixture `stepstest.BatchCronJob`. Conflict detection is narrower than other steps (checks `type`/`chainRef` only, not jobs content) — documented in CLAUDE.md as an accepted gap.
- Trigger step: new optional `fireOnCreate` param — when `"true"`, PATCHes the new `WeaveTrigger` with weave's fire annotation once, immediately after this call actually creates it (never on a call that adopts or re-confirms an already-existing trigger, so retries and a second run sharing the trigger never re-fire it). Added `WeaveClient.Fire` / `Weave.Fire` to the upstream interface for this. Closes the gap where fusion-wizard could not replicate spectra's "batch job starts once immediately" wizard behavior.
- New pre-seeded `WizardDefinition` `batch-git-job` (`definitions.batchGitJob.enabled`, default `true`): spectra's "Git Batch Job" wizard in catalogue form — one fixed trigger (`type`/`schedule` parameterised, `fireOnCreate: true`) instead of `python-git-job`'s per-entrypoint `forEach`. Matching reference fixture `stepstest.BatchJob`.

