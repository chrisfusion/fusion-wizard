# fusion-wizard

Kubernetes-native Go backend (operator + REST API) that orchestrates multi-service provisioning across
fusion-forge, fusion-index and fusion-weave (dir `../fusion-flux`), replacing the browser-side
orchestration in spectra's wizards (`useGitAppProvisioning.ts`, kept as proof of concept). Called by the BFFs.
Structural template is `../fusion-flux` (operator + api-server binaries, chart, auth); each sibling's
CLAUDE.md is authoritative for its own REST shapes. Module `fusion-platform.io/fusion-wizard`, GPL-3.0.

## Commands
- `make generate` — deepcopy + CRDs to `config/crd/bases/`; `make sync-crds` copies into the chart's `crds/` (manual: Helm never updates `crds/`); `make check-crds` diffs the two
- `make test` (`-race`, required: ledger/reconciler are concurrent); `make build`; `make docker-build` (minikube daemon, semver tag)
- Validate CRDs without persisting: `kubectl apply --dry-run=server -f config/crd/bases/`
- Local `go` is 1.22 but `go.mod` needs 1.25 — `GOTOOLCHAIN=auto` uses the cached toolchain; `controller-gen` is `~/go/bin` v0.16.1

## Design invariants (agreed, don't re-litigate)
- CRDs (`wizard.fusion-platform.io`): `WizardDefinition` (instance-agnostic recipe), `WizardRun` (state), `WizardResource` (ledger). Not built on weave.
- Step order is fixed (`gitWatcher, waitBuild, tag, jobTemplate, chain, trigger`); a definition picks a subset in that order.
- Per-instance values (runner image, resources, tag name, polling) come from a Helm-rendered ConfigMap at run time; precedence: run input > definition default > instance config.
- `WizardRun.spec.definitionSnapshot` is what runs and rollbacks use — never re-read the live definition mid-run.
- Ledger: every managed resource is ref-counted. Delete upstream only when the last ref is gone AND `managed: true`. Set `terminating` (resourceVersion-guarded) before deleting; adopters must not adopt a terminating entry. Found-without-ledger resources are `managed: false`, never deleted. Same name + different `specHash` = visible conflict, no silent update.
- Undo order is NOT blind reverse: by resource kind (`undoRank`, `internal/steps/rollback.go`): trigger, chain, template, **watcher**, tag, artifact — delete the watcher before the artifact or forge rebuilds it.
- Rollback is driven by the LEDGER (`Env.Rollback(run)` finds every entry referencing the run), never by run status: a step that failed midway holds a ref but is not in status. `SweepTerminating` finishes deletions a crash interrupted.
- `ensure()` is write-ahead: ledger entry first, upstream create second. A step's identity = the settings that define it (template: artifact+tag; NOT image/resources, which are instance defaults).
- Reconciler (`internal/controller`): one status `Patch` per reconcile, skipped when nothing changed (no write loops); the update predicate ignores status-only changes, polling is `RequeueAfter`; the retry annotation is removed only AFTER the reset status was written; a rolled-back run is terminal (create a new run).
- API (`internal/apiserver`): validates on create what the reconciler would validate later (definition, parameters, `plan.Expand`) so clients get every problem as one 422 with `details`; errors are `{"error","details"}` (spectra reads `body.error`); refuses to start without `AUTH_ALLOWED_SA` (no "any service account" mode); user headers are read only after the caller passed the allowlist; bulk rollback requires a selector and caps targets at 200.
- Operator calls upstreams (SA token re-read per request); api-server only reads/writes CRs. Identity (creator self-delete, privileged bulk delete) is deferred; API stamps `createdBy` from `X-User-ID` only for allowlisted BFF SAs.
- Every upstream resource the wizard creates is stamped `fusion-platform.io/managed-by: wizard` (platform-wide, open-valued — other components may use their own value, e.g. a future "manual" default) and `wizard.fusion-platform.io/run: <run-name>` (scoped, since weave already uses the unscoped `fusion-platform.io/run` for a different concept). Lets a raw `kubectl get -o yaml` answer "is this wizard-managed" without the ledger API — for warning a user before they hand-edit a chain or similar. `internal/ledger.LabelManagedBy`/`LabelRun`; applied in `weavesteps.go`'s `stampOwnerLabels` (jobTemplate/chain/trigger — one shared call site) and threaded to forge via `CreateGitWatcherRequest.Labels` (forge's `POST /api/v1/gitwatchers` accepts it as of 2026-09-22; its `PUT` never touches `ObjectMeta`, so labels survive updates).

## Gotchas
- `api.auth.audience` (`AUTH_AUDIENCE`) is NOT optional in practice despite the values.yaml comment: an
  empty value makes `TokenReviewAuthenticator` omit `spec.audiences`, and Kubernetes then validates the
  caller's token against the **apiserver's own default audience** (`https://kubernetes.default.svc...`),
  not "any audience". fusion-bff's default upstream token (`saToken`, shared with forge/index/content) is
  minted for the custom audience `fusion-bff` (`fusion-bff/deployment/templates/deployment.yaml`'s
  `sa-token` projected volume) — with `AUTH_AUDIENCE` unset, every call from that token fails with
  `invalid token` (401) and Kubernetes' real reason only surfaces via a debug log line (`internal/apiserver/auth.go`'s `Authenticate`, added 2026-09-22:
  logs `tr.Status.Error` on rejection) — the plain 401 response body gives no clue. Confirmed
  experimentally (toggled `AUTH_AUDIENCE` empty vs `fusion-bff` against the same live token): set
  `api.auth.audience=fusion-bff` (or whatever the calling BFF's token audience is) whenever the caller
  uses a custom-audience token rather than its pod's default automounted one.
- Every status field `+optional`, else use `Status().Update()` (required-field `Patch` deadlock, see fusion-flux CLAUDE.md); finalizers via `Patch`, never `Update`
- printcolumn JSONPath has no `length()`; list-map keys (`name`, `key`) must be required fields
- `go mod tidy` prunes deps nothing imports yet — re-run after adding code
- Ledger writes use `conflictBackoff` (30 steps), not `retry.DefaultRetry` (5): shared entries are contended by design and the default failed a 25-writer test
- Controller/steps tests share `internal/steps/stepstest` fakes; the controller harness injects an `EnvFactory` (`staticEnv`). The fake client needs `WithStatusSubresource(&WizardRun{})` and does not maintain `metadata.generation`
- `forEach` keys are `<step>/<item>` (`etl/load.py` -> key `trigger/etl/load.py`; `stem` keeps directories, so trigger names come out `<job>-etl-load`)
- Mutation-check safety tests from a FRESH copy of the tree each time (a helper that restores only the mutated file lets earlier mutations leak into later runs)
- Docker check: `docker build` with the local daemon (not minikube's), run both binaries with `--help`, then `docker rmi`
- Tests that call `Ensure` need a rig with NO pre-seeded upstream objects if they expect the wizard to own (and delete) them — pre-seeded ones are correctly treated as unowned
- Weave specs are generic `map[string]any` (no import of fusion-weave types); `waitBuild` matches builds by repoUrl+projectDir because forge clears `lastBuildName` on success
- `config/rbac/` and chart RBAC templates are hand-maintained pairs
- Forge's `GitWatcher` reconciler self-heals a build whose index artifact vanished (e.g. deleted by our rollback): it rebuilds on its own next tick, but only once `HEAD` changes or on a brand-new watcher's first reconcile — a dormant repo needs forge's `POST /api/v1/builds/index-drift-cleanup` backstop instead. `waitBuild` retries a missing-artifact build up to `BuildTimeout` instead of failing immediately, to give forge a chance to catch up (verified end-to-end in minikube 2026-09-22)
- `minikube image load <name>:<tag>` (by name) can silently keep a stale cached image even though it reports success; `docker save -o x.tar <name>:<tag>` then `minikube image load x.tar` reliably picks up a rebuilt image
- zsh: `status` is a read-only special var — don't use it as a loop variable (`st=$(...)` instead); unquoted `?` in curl URLs gets glob-expanded ("no matches found") — quote the URL

## Rules
- `fusion-dev-a`/`fusion-dev-b` are Flux-managed — never `helm upgrade` those; e2e in a disposable namespace. `fusion` itself is non-Flux, the user's own dev/test instance — direct `helm upgrade`/deploys there are fine (confirmed 2026-09-22)
- CHANGELOG entry (Keep a Changelog, `## [x.y.z] — YYYY-MM-DD`) before every commit; bump chart `version`/`appVersion` on release
- Branch is `main`; ask before commit/push; no Claude co-author trailer; SPDX header on every `.go` file
