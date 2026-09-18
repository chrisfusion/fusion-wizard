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
