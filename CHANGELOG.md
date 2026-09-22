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

### Fixed
- `waitBuild` no longer fails a run permanently when forge reports a build `SUCCESS` but its index artifact is missing (e.g. deleted by our own rollback): forge's `GitWatcher` reconciler now self-heals this case on its own (see fusion-forge's `index-drift-cleanup`), just not synchronously with our poll, so `waitBuild` retries up to `BuildTimeout` instead of giving up immediately. Verified end-to-end against a real forge/index in minikube.

