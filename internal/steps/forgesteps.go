// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const buildListSize = 100

// notFoundAsNil turns an upstream 404 into (nil, nil): "does not exist" is an answer, not an error.
func notFoundAsNil[T any](v *T, err error) (*T, error) {
	if errors.Is(err, upstream.ErrNotFound) {
		return nil, nil
	}
	return v, err
}

// ---- gitWatcher ----

type gitWatcherStep struct{}

func (gitWatcherStep) Type() wizardv1.StepType { return wizardv1.StepGitWatcher }
func (gitWatcherStep) Outputs() []string       { return []string{"name"} }
func (gitWatcherStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name", "repoUrl"}, Optional: []string{"repoRef", "projectDir"}}
}

// Ensure creates the forge GitWatcher (build type "app": name and version come from the repo's
// metadata.yaml). Forge, not the wizard, then builds whenever the repository's version changes.
func (gitWatcherStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, repoURL := in.Params["name"], in.Params["repoUrl"]
	repoRef, projectDir := in.Params["repoRef"], in.Params["projectDir"]

	return env.ensureStep(ctx, in, managed{
		Key:  ledger.Key{Service: wizardv1.ServiceForge, Kind: KindGitWatcher, Name: name},
		Hash: ledger.Hash(KindGitWatcher, repoURL, repoRef, projectDir),
		Get: func(ctx context.Context) (*existing, error) {
			gw, err := notFoundAsNil(env.Forge.GetGitWatcher(ctx, name))
			if gw == nil || err != nil {
				return nil, err
			}
			// Same check as spectra's ensureGitWatcher: the repository and subfolder identify the
			// watcher. repoRef is compared through the ledger hash for wizard-made watchers.
			return &existing{
				Matches: gw.Spec.BuildType == "app" && gw.Spec.RepoURL == repoURL && gw.Spec.ProjectDir == projectDir,
				Detail:  "it watches a different repository or subfolder",
			}, nil
		},
		Create: func(ctx context.Context) (string, error) {
			_, err := env.Forge.CreateGitWatcher(ctx, upstream.CreateGitWatcherRequest{
				Name: name, RepoURL: repoURL, RepoRef: repoRef, BuildType: "app", ProjectDir: projectDir,
			})
			return "", err
		},
	}, map[string]string{"name": name})
}

// ---- waitBuild ----

type waitBuildStep struct{}

func (waitBuildStep) Type() wizardv1.StepType { return wizardv1.StepWaitBuild }
func (waitBuildStep) Outputs() []string {
	return []string{"buildId", "name", "artifactName", "artifactId", "version"}
}
func (waitBuildStep) Params() ParamSpec { return ParamSpec{Required: []string{"watcher"}} }

// Ensure waits for forge to finish the build of the watcher's repository and then claims the index
// artifact forge produced, so a rollback removes it. It never blocks: it returns a not-done Result
// and is called again after the poll interval.
func (waitBuildStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	watcher := in.Params["watcher"]

	gw, err := env.Forge.GetGitWatcher(ctx, watcher)
	if err != nil {
		return Result{}, err
	}
	if gw.Status.Phase == "Disabled" {
		return Result{}, permanentf("forge disabled watcher %q after %d failed build attempts: %s",
			watcher, gw.Status.ConsecutiveFailures, firstNonEmpty(gw.Status.LastError, gw.Status.Message, "no error reported"))
	}
	if !in.StartedAt.IsZero() && env.now().Sub(in.StartedAt) > env.Cfg.BuildTimeout {
		return Result{}, permanentf("timed out after %s waiting for the build of watcher %q", env.Cfg.BuildTimeout, watcher)
	}

	build, err := findBuild(ctx, env, gw, in.Prior["buildId"])
	if err != nil {
		return Result{}, err
	}
	if build == nil {
		return Result{Requeue: env.Cfg.PollInterval, Message: "waiting for forge to start a build"}, nil
	}
	progress := map[string]string{"buildId": strconv.FormatInt(build.ID, 10)}

	switch build.Status {
	case "SUCCESS":
	case "FAILED":
		return Result{}, permanentf("build %d of watcher %q failed; see the forge build logs", build.ID, watcher)
	default:
		return Result{Requeue: env.Cfg.PollInterval, Message: fmt.Sprintf("build %d is %s", build.ID, build.Status), Outputs: progress}, nil
	}

	if build.IndexArtifactID == nil {
		return Result{}, permanentf("build %d succeeded but reported no index artifact", build.ID)
	}
	artifactName := env.Cfg.ArtifactPrefix + build.Name
	version := build.Version
	if build.IndexArtifactVersion != nil {
		version = *build.IndexArtifactVersion
	}

	// Forge's GitWatcher reconciler self-heals a build row whose index artifact vanished (e.g.
	// deleted by our own rollback): it rebuilds on its own next reconcile tick, which is not
	// synchronous with this poll. Retry instead of failing outright; BuildTimeout above still
	// bounds it if forge never catches up.
	art, err := env.Index.FindArtifact(ctx, artifactName)
	if errors.Is(err, upstream.ErrNotFound) {
		return Result{Requeue: env.Cfg.PollInterval, Outputs: progress,
			Message: fmt.Sprintf("build %d succeeded but artifact %q is missing from the index; waiting for forge to notice and rebuild", build.ID, artifactName)}, nil
	}
	if err != nil {
		return Result{}, err
	}
	artifactID := strconv.FormatInt(art.ID, 10)

	outputs := map[string]string{
		"buildId": progress["buildId"], "name": build.Name, "artifactName": artifactName,
		"artifactId": artifactID, "version": version,
	}
	return env.ensureStep(ctx, in, managed{
		Key:   ledger.Key{Service: wizardv1.ServiceIndex, Kind: KindArtifact, Name: artifactName},
		Claim: true,
		Get: func(context.Context) (*existing, error) {
			return &existing{ExternalID: artifactID, Matches: true}, nil
		},
		Create: func(context.Context) (string, error) { // unreachable: the artifact was just found
			return "", permanentf("artifact %q vanished while it was being claimed", artifactName)
		},
	}, outputs)
}

// findBuild locates the build to wait for. A remembered ID is polled directly (the only call that
// makes forge sync build status). If forge deleted that row (it does so when it retries a failed
// build) or nothing is remembered, the newest app build of the watcher's repository is used; list
// rows can be stale, so it is re-read by ID.
func findBuild(ctx context.Context, env *Env, gw *upstream.GitWatcher, remembered string) (*upstream.Build, error) {
	if id, err := strconv.ParseInt(remembered, 10, 64); err == nil {
		b, err := notFoundAsNil(env.Forge.GetAppBuild(ctx, id))
		if b != nil || err != nil {
			return b, err
		}
	}
	builds, err := env.Forge.ListAppBuilds(ctx, "", buildListSize)
	if err != nil {
		return nil, err
	}
	for _, b := range builds { // forge lists newest first
		if b.RepoURL != nil && *b.RepoURL == gw.Spec.RepoURL && b.BuildType == "app" && sameProjectDir(b.ProjectDir, gw.Spec.ProjectDir) {
			return notFoundAsNil(env.Forge.GetAppBuild(ctx, b.ID))
		}
	}
	return nil, nil
}

func sameProjectDir(build *string, watcher string) bool {
	if build == nil {
		return watcher == ""
	}
	return *build == watcher
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
