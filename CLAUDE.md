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
- Operator calls upstreams (SA token re-read per request); api-server only reads/writes CRs. Identity (creator self-delete, privileged bulk delete) is deferred; API stamps `createdBy` from `X-User-ID` only for allowlisted BFF SAs.

## Gotchas
- Every status field `+optional`, else use `Status().Update()` (required-field `Patch` deadlock, see fusion-flux CLAUDE.md); finalizers via `Patch`, never `Update`
- printcolumn JSONPath has no `length()`; list-map keys (`name`, `key`) must be required fields
- `go mod tidy` prunes deps nothing imports yet — re-run after adding code
- Ledger writes use `conflictBackoff` (30 steps), not `retry.DefaultRetry` (5): shared entries are contended by design and the default failed a 25-writer test
- Tests that call `Ensure` need a rig with NO pre-seeded upstream objects if they expect the wizard to own (and delete) them — pre-seeded ones are correctly treated as unowned
- Weave specs are generic `map[string]any` (no import of fusion-weave types); `waitBuild` matches builds by repoUrl+projectDir because forge clears `lastBuildName` on success
- `config/rbac/` and chart RBAC templates are hand-maintained pairs
- Open risk: forge has no per-build delete, so re-running a rolled-back version can hit "version already built in DB — skipping"

## Rules
- Never `helm upgrade` live `fusion`/`fusion-dev-a`/`fusion-dev-b`; e2e in a disposable namespace
- CHANGELOG entry (Keep a Changelog, `## [x.y.z] — YYYY-MM-DD`) before every commit; bump chart `version`/`appVersion` on release
- Branch is `main`; ask before commit/push; no Claude co-author trailer; SPDX header on every `.go` file
