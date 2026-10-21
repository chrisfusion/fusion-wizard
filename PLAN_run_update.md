# Task: update a wizard run in place ("Configure")

Status: **decided, not started** (2026-10-21). Plan the details before coding.

## Why

A wizard run can only be created, retried, rolled back or deleted. To change anything it created
(image, port, schedule, …) a user must roll back and start a new run — downtime, a rebuild for git
wizards, a new run name. Spectra's Weave → Apps page works around this for the image by PUTting the
WeaveApp directly, which drifts from the ledger (a retry may restore the old image).

## Goal

`PATCH /api/v1/runs/{name}` with new `parameters`: the run re-applies its steps and updates the
upstream resources it owns, keeping its name and history. Spectra gets a **Configure** button on the
wizard run page (form pre-filled, unchangeable fields locked).

## Rules (agreed)

- Relaxes "same name + different specHash = conflict, no silent update" **only** for a resource this
  run solely owns (`managed: true`, ref count 1): update upstream (PUT/PATCH) and store the new
  `specHash`. A shared or unmanaged resource stays a visible conflict, as today.
- Definitions mark which parameters may change (e.g. `mutable: true` on `WizardParameter`).
  Name-forming and identity params stay fixed: `serviceName`, `jobName`, `repoUrl`, … Changeable
  candidates: `image`, `port`, `ingressName`, `schedule`, `entrypoints`, `externalAuth*`, `autoPromote`.
- Validation on PATCH is the same as on create (one 422 with `details`); changing a fixed param is a 422.
- Only `Ready` (and maybe `Failed`) runs can be updated; `RolledBack` stays terminal.

## Open questions

- Which step types support update first? Suggest `app` only (covers image-service/job/cron-job and
  python-git-job/service), then `gitWatcher` (`autoPromote`), later template/chain/trigger.
- Update semantics per upstream: weave `PUT /apps/{name}` needs the fresh `resourceVersion`;
  `fireOnCreate` must not fire again on update.
- How the run shows an update in progress (new phase vs. `Applying` again) and keeps a history of
  parameter changes (who/when).
- BFF: new route rule + permission (e.g. `wizard:runs:update`) in `rbac.yaml`.
- Afterwards: switch Spectra's Apps page "Change image" for wizard-managed apps to this endpoint and
  drop the drift warning.

## Touches

fusion-wizard (API, CRD types/`mutable`, reconciler, ledger, step update paths, chart definitions,
tests), fusion-bff (`rbac.yaml`), fusion-spectra (Configure button, form pre-fill, Apps page).
