// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"strings"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

type runStep struct{}

func (runStep) Type() wizardv1.StepType { return wizardv1.StepRun }
func (runStep) Outputs() []string       { return []string{"name"} }
func (runStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name", "chain", "image"}, Optional: []string{"stepName", "stepKind", "imagePullPolicy", "ingressName"}}
}

// Step kinds of the chain step a run overrides. A Deploy step becomes a run-owned, image-only
// Deployment; a Job step needs no stepOverrides entry (jobs are not run-owned), its imageOverrides
// entry alone changes the image of the Job the run creates.
const (
	stepKindDeploy = "Deploy"
	stepKindJob    = "Job"
)

// Ensure creates a WeaveRun that runs the chain's step with the given image: for a Deploy step
// (default) as a run-owned, image-only Deployment (<run>-<step>), for a Job step (stepKind "Job") as
// a one-shot Job that starts right away. The chain and its template are shared by every run, so n
// images cost one template, one chain and n runs. A trigger cannot do this: only a WeaveRun carries
// stepOverrides and imageOverrides.
//
// Weave owns the validation of the allowlist prefix (ALLOWED_IMAGE_PREFIXES) and rejects a bad image
// with a 400, which fails the step permanently; the tag/digest rule is also checked up front by
// ValidateImageParams. Deleting the run removes its Deployment, Service and Ingress through
// their owner references, so the ledger's delete of this resource is the whole teardown.
func (runStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, chain, image := in.Params["name"], in.Params["chain"], in.Params["image"]
	ingressName, pullPolicy := in.Params["ingressName"], in.Params["imagePullPolicy"]
	stepName := firstNonEmpty(in.Params["stepName"], env.Cfg.ChainStepName)
	stepKind := firstNonEmpty(in.Params["stepKind"], stepKindDeploy)
	if err := ValidateImageParams(wizardv1.StepRun, in.Params); err != nil {
		return Result{}, err
	}

	imageOverride := map[string]any{"stepName": stepName, "image": image}
	if pullPolicy != "" {
		imageOverride["imagePullPolicy"] = pullPolicy
	}
	spec := map[string]any{
		"chainRef":       map[string]any{"name": chain},
		"imageOverrides": []any{imageOverride},
	}
	if stepKind == stepKindDeploy {
		stepOverride := map[string]any{"stepName": stepName}
		if ingressName != "" {
			stepOverride["ingressName"] = ingressName
		}
		spec["stepOverrides"] = []any{stepOverride}
	}

	// A Deploy run keeps its original hash parts, so entries written before Job mode existed still
	// match; a Job run adds its kind.
	hashParts := []string{KindRun, chain, stepName, image, pullPolicy, ingressName}
	if stepKind == stepKindJob {
		hashParts = append(hashParts, stepKindJob)
	}

	return env.weaveResource(ctx, in, KindRun, name,
		ledger.Hash(hashParts...),
		upstream.NewWeaveObject("WeaveRun", name, spec),
		func(existing map[string]any) string {
			images, _ := nested(existing, "imageOverrides").([]any)
			overrides, _ := nested(existing, "stepOverrides").([]any)
			switch {
			case nestedString(existing, "chainRef", "name") != chain:
				return "it runs a different chain"
			case firstEntryString(images, "image") != image || firstEntryString(images, "imagePullPolicy") != pullPolicy:
				return "it runs a different image"
			case firstEntryString(overrides, "ingressName") != ingressName:
				return "it has a different ingress"
			case (len(overrides) == 0) != (stepKind == stepKindJob):
				return "it runs a different kind of step"
			}
			return ""
		}, nil, map[string]string{"name": name})
}

// firstEntryString reads one string field of the first object in a weave-returned list ("" when the
// list is empty or the field is absent).
func firstEntryString(list []any, field string) string {
	if len(list) == 0 {
		return ""
	}
	m, _ := list[0].(map[string]any)
	return nestedString(m, field)
}

// ValidateImageParams checks the image params of a run or trigger step (any other step type passes):
// the image needs an explicit tag other than "latest", or a digest, the same rule weave enforces on
// imageOverrides. A trigger's image is optional (none means no override), a run's is required. The allowlist prefix is deliberately not checked here, it is weave's deploy-time
// setting. Params that are already resolved are enough, so the API can reject a bad image at run
// creation instead of after the earlier steps have run.
func ValidateImageParams(typ wizardv1.StepType, params map[string]string) error {
	if typ != wizardv1.StepRun && typ != wizardv1.StepTrigger {
		return nil
	}
	image := params["image"]
	if typ == wizardv1.StepTrigger && image == "" {
		if params["imagePullPolicy"] != "" {
			return permanentf("imagePullPolicy needs an image")
		}
		return nil
	}
	switch {
	case image == "" || strings.ContainsAny(image, " \t\n"):
		return permanentf("image %q is empty or contains whitespace", image)
	case !hasImmutableRef(image):
		return permanentf("image %q must have an explicit tag (not \":latest\") or a digest", image)
	}
	switch p := params["imagePullPolicy"]; p {
	case "", "Always", "IfNotPresent", "Never":
	default:
		return permanentf("imagePullPolicy %q is not supported (use Always, IfNotPresent or Never)", p)
	}
	switch k := params["stepKind"]; k {
	case "", stepKindDeploy:
	case stepKindJob:
		if typ == wizardv1.StepRun && params["ingressName"] != "" {
			return permanentf("a Job run has no ingress (ingressName %q)", params["ingressName"])
		}
	default:
		return permanentf("stepKind %q is not supported (use Deploy or Job)", k)
	}
	return nil
}

// hasImmutableRef mirrors fusion-weave's internal/imagepolicy: a digest, or a tag other than latest.
func hasImmutableRef(image string) bool {
	if strings.Contains(image, "@sha256:") {
		return true
	}
	// The tag follows the last ":" after the last "/" (an earlier ":" is a registry host:port).
	name := image[strings.LastIndex(image, "/")+1:]
	i := strings.LastIndex(name, ":")
	if i < 0 {
		return false
	}
	tag := name[i+1:]
	return tag != "" && tag != "latest"
}

// ValidatesEarly reports whether the API can validate this param of a step at run creation
// (ValidateExternalAuthParams / ValidateImageParams), when it resolves from run parameters alone.
func ValidatesEarly(typ wizardv1.StepType, key string) bool {
	switch typ {
	case wizardv1.StepChain:
		return strings.HasPrefix(key, "externalAuth")
	case wizardv1.StepTrigger:
		return strings.HasPrefix(key, "externalAuth") || key == "image" || key == "imagePullPolicy"
	case wizardv1.StepRun:
		return key == "image" || key == "imagePullPolicy" || key == "stepKind" || key == "ingressName"
	}
	return false
}
