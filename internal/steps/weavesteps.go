// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

const overridePrefix = "override."

// weaveResource ensures one weave custom resource. matches inspects the existing object's spec and
// returns what differs ("" when it is what this step wants). The weave API forces the namespace, so
// the wizard only supplies the name.
// afterCreate, when non-nil, runs once right after Create's upstream POST succeeds — never on a
// call that instead adopts or re-confirms an already-existing resource (that path never reaches
// Create at all). Used by the trigger step's fireOnCreate.
func (e *Env) weaveResource(ctx context.Context, in Input, kind, name, hash string, obj upstream.WeaveObject,
	matches func(spec map[string]any) string, afterCreate func(ctx context.Context) error, outputs map[string]string) (Result, error) {

	collection := weaveCollection(kind)
	return e.ensureStep(ctx, in, managed{
		Key:  ledger.Key{Service: wizardv1.ServiceWeave, Kind: kind, Name: name},
		Hash: hash,
		Get: func(ctx context.Context) (*existing, error) {
			found, err := e.Weave.Get(ctx, collection, name)
			if errors.Is(err, upstream.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			spec, _ := found["spec"].(map[string]any)
			diff := matches(spec)
			return &existing{Matches: diff == "", Detail: diff}, nil
		},
		Create: func(ctx context.Context) (string, error) {
			stampOwnerLabels(obj, in.Run)
			if _, err := e.Weave.Create(ctx, collection, obj); err != nil {
				return "", err
			}
			if afterCreate != nil {
				if err := afterCreate(ctx); err != nil {
					return "", err
				}
			}
			return "", nil
		},
	}, outputs)
}

// stampOwnerLabels marks a generic upstream object as wizard-managed, so a bare `kubectl get -o
// yaml` shows ownership without querying the wizard's own ledger API.
func stampOwnerLabels(obj upstream.WeaveObject, run string) {
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	meta["labels"] = map[string]any{
		ledger.LabelManagedBy: ledger.ManagedByWizard,
		ledger.LabelRun:       run,
	}
}

// ---- jobTemplate ----

type jobTemplateStep struct{}

func (jobTemplateStep) Type() wizardv1.StepType { return wizardv1.StepJobTemplate }
func (jobTemplateStep) Outputs() []string       { return []string{"name"} }
func (jobTemplateStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name"}, Optional: []string{"artifactName", "tag", "image"}}
}

// Ensure creates a WeaveJobTemplate that runs the built artifact. The identity of a template is
// the artifact and tag it runs; the runner image and resources are instance defaults and are not
// part of the identity, so changing them in the instance config never turns an existing shared
// template into a conflict.
//
// artifactName and tag (both or neither) select the mode. Without them the template is image-only:
// no codeSource, so the Job runs whatever image a run's imageOverrides supplies (see the run step)
// and nothing is loaded from fusion-index.
func (jobTemplateStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, artifactName, tag := in.Params["name"], in.Params["artifactName"], in.Params["tag"]
	image := firstNonEmpty(in.Params["image"], env.Cfg.RunnerImage)
	if (artifactName == "") != (tag == "") {
		return Result{}, permanentf("jobTemplate %q needs artifactName and tag together, or neither (image-only)", name)
	}

	spec := map[string]any{"image": image}
	// Artifact mode keeps its original hash parts, so entries written before image-only mode existed
	// still match.
	hashParts := []string{KindJobTemplate, artifactName, tag}
	if artifactName != "" {
		spec["codeSource"] = map[string]any{"artifactName": artifactName, "tag": tag}
	} else {
		hashParts = []string{KindJobTemplate, "image-only"}
	}
	res, err := resourcesMap(env.Cfg.DefaultResources)
	if err != nil {
		return Result{}, err
	}
	if res != nil {
		spec["resources"] = res
	}

	return env.weaveResource(ctx, in, KindJobTemplate, name, ledger.Hash(hashParts...),
		upstream.NewWeaveObject("WeaveJobTemplate", name, spec),
		func(existing map[string]any) string {
			if nestedString(existing, "codeSource", "artifactName") != artifactName || nestedString(existing, "codeSource", "tag") != tag {
				return "it runs a different artifact or tag"
			}
			return ""
		}, nil, map[string]string{"name": name})
}

// ---- serviceTemplate ----

type serviceTemplateStep struct{}

func (serviceTemplateStep) Type() wizardv1.StepType { return wizardv1.StepServiceTemplate }
func (serviceTemplateStep) Outputs() []string       { return []string{"name"} }
func (serviceTemplateStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name", "port"}, Optional: []string{"artifactName", "tag", "image", "ingressName"}}
}

// servicePortName is the WeaveServiceTemplate port's own name, referenced by an ingress rule's
// servicePort. The wizard exposes exactly one port per service, so a fixed name is enough.
const servicePortName = "http"

// Ensure creates a WeaveServiceTemplate that runs the built artifact as a long-running service
// (e.g. a Streamlit app), instead of a one-shot Job. Unlike jobTemplate, the port and ingress name
// are part of the template's identity alongside the artifact and tag: they change what the service
// actually exposes, not just an instance default like the runner image.
//
// artifactName and tag (both or neither) select the mode. Without them the template is image-only:
// no codeSource, so the Deployment runs whatever image a run's imageOverrides supplies (see the run
// step) and nothing is loaded from fusion-index.
func (serviceTemplateStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, artifactName, tag := in.Params["name"], in.Params["artifactName"], in.Params["tag"]
	portStr, ingressName := in.Params["port"], in.Params["ingressName"]
	image := firstNonEmpty(in.Params["image"], env.Cfg.RunnerImage)

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Result{}, permanentf("port %q is not a whole number", portStr)
	}
	if (artifactName == "") != (tag == "") {
		return Result{}, permanentf("serviceTemplate %q needs artifactName and tag together, or neither (image-only)", name)
	}

	spec := map[string]any{
		"image": image,
		"ports": []any{map[string]any{"name": servicePortName, "port": port}},
	}
	// Artifact mode keeps its original hash parts, so entries written before image-only mode existed
	// still match.
	hashParts := []string{KindServiceTemplate, artifactName, tag, portStr, ingressName}
	if artifactName != "" {
		spec["codeSource"] = map[string]any{"artifactName": artifactName, "tag": tag}
	} else {
		hashParts = []string{KindServiceTemplate, "image-only", portStr, ingressName}
	}
	res, err := resourcesMap(env.Cfg.DefaultResources)
	if err != nil {
		return Result{}, err
	}
	if res != nil {
		spec["resources"] = res
	}
	if ingressName != "" {
		spec["ingress"] = map[string]any{"rules": []any{map[string]any{"name": ingressName, "servicePort": servicePortName}}}
	}

	return env.weaveResource(ctx, in, KindServiceTemplate, name,
		ledger.Hash(hashParts...),
		upstream.NewWeaveObject("WeaveServiceTemplate", name, spec),
		func(existing map[string]any) string {
			switch {
			case nestedString(existing, "codeSource", "artifactName") != artifactName || nestedString(existing, "codeSource", "tag") != tag:
				return "it runs a different artifact or tag"
			case existingPort(existing) != portStr:
				return "it exposes a different port"
			case existingIngressName(existing) != ingressName:
				return "it has a different ingress"
			}
			return ""
		}, nil, map[string]string{"name": name})
}

// existingPort reads the first port's number back out of a weave-returned spec, as a string so it
// compares directly against the resolved param (JSON numbers decode as float64).
func existingPort(spec map[string]any) string {
	ports, _ := nested(spec, "ports").([]any)
	if len(ports) == 0 {
		return ""
	}
	m, ok := ports[0].(map[string]any)
	if !ok {
		return ""
	}
	switch v := m["port"].(type) {
	case int: // in-memory fakes keep the typed value; real weave JSON decodes to float64
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	default:
		return ""
	}
}

func existingIngressName(spec map[string]any) string {
	rules, _ := nested(spec, "ingress", "rules").([]any)
	if len(rules) == 0 {
		return ""
	}
	m, ok := rules[0].(map[string]any)
	if !ok {
		return ""
	}
	return nestedString(m, "name")
}

// ---- chain ----

type chainStep struct{}

func (chainStep) Type() wizardv1.StepType { return wizardv1.StepChain }
func (chainStep) Outputs() []string       { return []string{"name"} }
func (chainStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name"}, Optional: []string{"jobTemplate", "serviceTemplate", "stepName", "externalAuthMode", "externalAuthName"}}
}

// Ensure creates a WeaveChain with one step referencing either the job template (a one-shot Job,
// fired by a trigger) or the service template (a long-running Deploy, also fired once by a trigger
// but then owned by the chain itself, outliving the WeaveRun that created it). Exactly one of the
// two must be given.
//
// externalAuthMode/externalAuthName (both or neither) set the chain's externalAuthRef: weave mints
// a short-lived token for the named allowlisted ServiceAccount or OIDC secret and injects it into
// every Job pod. Not offered for a service template: the token would expire under a long-running
// Deploy.
func (chainStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name := in.Params["name"]
	jobTemplate, serviceTemplate := in.Params["jobTemplate"], in.Params["serviceTemplate"]
	stepName := firstNonEmpty(in.Params["stepName"], env.Cfg.ChainStepName)

	var template, ref, stepKind string
	switch {
	case jobTemplate != "" && serviceTemplate != "":
		return Result{}, permanentf("chain step %q cannot have both jobTemplate and serviceTemplate", name)
	case jobTemplate != "":
		template, ref, stepKind = jobTemplate, "jobTemplateRef", "Job"
	case serviceTemplate != "":
		template, ref, stepKind = serviceTemplate, "serviceTemplateRef", "Deploy"
	default:
		return Result{}, permanentf("chain step %q needs either jobTemplate or serviceTemplate", name)
	}

	authRef, err := externalAuthFromParams(in.Params, "externalAuthMode", "externalAuthName")
	if err != nil {
		return Result{}, err
	}
	if authRef != nil && stepKind == "Deploy" {
		return Result{}, permanentf("externalAuth is not supported for a service template (its token would expire under a long-running deployment)")
	}

	chainStepSpec := map[string]any{"name": stepName, "stepKind": stepKind, ref: map[string]any{"name": template}}
	if stepKind == "Deploy" {
		chainStepSpec["runOnSuccess"] = true
	}

	spec := map[string]any{"steps": []any{chainStepSpec}}
	if authRef != nil {
		spec["externalAuthRef"] = authRef
	}
	return env.weaveResource(ctx, in, KindChain, name, ledger.Hash(authHashParts(authRef, KindChain, ref, template, stepName)...),
		upstream.NewWeaveObject("WeaveChain", name, spec),
		func(existing map[string]any) string {
			if existingAuthKey(existing, "externalAuthRef") != authRefKey(authRef) {
				return "it has a different externalAuthRef"
			}
			steps, _ := nested(existing, "steps").([]any)
			for _, s := range steps {
				if m, ok := s.(map[string]any); ok && nestedString(m, ref, "name") == template {
					return ""
				}
			}
			return "it does not run this template"
		}, nil, map[string]string{"name": name})
}

// ---- trigger ----

type triggerStep struct{}

func (triggerStep) Type() wizardv1.StepType { return wizardv1.StepTrigger }
func (triggerStep) Outputs() []string       { return []string{"name"} }
func (triggerStep) Params() ParamSpec {
	return ParamSpec{Required: []string{"name", "chain"}, Optional: []string{"type", "schedule", "fireOnCreate", "externalAuthOverrideMode", "externalAuthOverrideName", "image", "imagePullPolicy", "stepName"}, Prefixes: []string{overridePrefix}}
}

// Ensure creates a WeaveTrigger (OnDemand or Cron) for the chain. Params prefixed "override."
// become environment overrides injected into every run the trigger creates, e.g.
// "override.ENTRYPOINT" -> parameterOverrides [{name: ENTRYPOINT, value: ...}]. Unlike spectra,
// an existing trigger with a different schedule or overrides is a conflict, not silently reused.
//
// fireOnCreate: "true" asks weave to fire one run immediately, but only the call that actually
// creates the trigger does so — adopting or re-confirming an existing one never re-fires it, so a
// later reconcile of this same step (or a second run sharing the trigger) is a no-op here.
//
// externalAuthOverrideMode/externalAuthOverrideName (both or neither) set the trigger's
// externalAuthRefOverride, which takes precedence over the chain's externalAuthRef.
//
// image (with optional imagePullPolicy and stepName, default the chain's step name) sets the
// trigger's imageOverrides: weave copies it into every run the trigger creates, so a shared image-only
// job template can run a caller-supplied image. Job steps only (a trigger cannot make a Deployment
// run-owned). Weave checks the tag rule and the allowed prefixes.
func (triggerStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	name, chain := in.Params["name"], in.Params["chain"]
	typ := firstNonEmpty(in.Params["type"], "OnDemand")
	schedule := in.Params["schedule"]
	fireOnCreate := in.Params["fireOnCreate"] == "true"
	switch {
	case typ != "OnDemand" && typ != "Cron":
		return Result{}, permanentf("trigger type %q is not supported (use OnDemand or Cron)", typ)
	case typ == "Cron" && schedule == "":
		return Result{}, permanentf("a Cron trigger needs a schedule")
	case typ != "Cron" && schedule != "":
		return Result{}, permanentf("schedule is only valid for a Cron trigger")
	}

	overrides := map[string]string{}
	for k, v := range in.Params {
		if strings.HasPrefix(k, overridePrefix) {
			overrides[strings.TrimPrefix(k, overridePrefix)] = v
		}
	}
	pairs := sortedPairs(overrides)

	authRef, err := externalAuthFromParams(in.Params, "externalAuthOverrideMode", "externalAuthOverrideName")
	if err != nil {
		return Result{}, err
	}

	if err := ValidateImageParams(wizardv1.StepTrigger, in.Params); err != nil {
		return Result{}, err
	}
	image, pullPolicy := in.Params["image"], in.Params["imagePullPolicy"]
	imageStep := firstNonEmpty(in.Params["stepName"], env.Cfg.ChainStepName)

	spec := map[string]any{"chainRef": map[string]any{"name": chain}, "type": typ}
	hashParts := []string{KindTrigger, chain, typ, schedule}
	if image != "" {
		override := map[string]any{"stepName": imageStep, "image": image}
		if pullPolicy != "" {
			override["imagePullPolicy"] = pullPolicy
		}
		spec["imageOverrides"] = []any{override}
		// Only set when used, so triggers written before image overrides existed keep their hash.
		hashParts = append(hashParts, "image", imageStep, image, pullPolicy)
	}
	if authRef != nil {
		spec["externalAuthRefOverride"] = authRef
	}
	if schedule != "" {
		spec["schedule"] = schedule
	}
	if len(overrides) > 0 {
		var list []any
		for _, k := range sortedKeys(overrides) {
			list = append(list, map[string]any{"name": k, "value": overrides[k]})
		}
		spec["parameterOverrides"] = list
	}

	var afterCreate func(context.Context) error
	if fireOnCreate {
		afterCreate = func(ctx context.Context) error { return env.Weave.Fire(ctx, name) }
	}

	return env.weaveResource(ctx, in, KindTrigger, name,
		ledger.Hash(authHashParts(authRef, append(hashParts, pairs...)...)...),
		upstream.NewWeaveObject("WeaveTrigger", name, spec),
		func(existing map[string]any) string {
			switch {
			case nestedString(existing, "chainRef", "name") != chain:
				return "it triggers a different chain"
			case nestedString(existing, "type") != typ || nestedString(existing, "schedule") != schedule:
				return "it has a different type or schedule"
			case strings.Join(existingPairs(existing), "\x00") != strings.Join(pairs, "\x00"):
				return "it has different parameter overrides"
			case existingAuthKey(existing, "externalAuthRefOverride") != authRefKey(authRef):
				return "it has a different externalAuthRefOverride"
			case existingImageKey(existing) != imageKey(imageStep, image, pullPolicy):
				return "it has different image overrides"
			}
			return ""
		}, afterCreate, map[string]string{"name": name})
}

// imageKey is the comparable identity of a trigger's image override ("" when none is wanted).
func imageKey(stepName, image, pullPolicy string) string {
	if image == "" {
		return ""
	}
	return stepName + "\x00" + image + "\x00" + pullPolicy
}

// existingImageKey reads the first imageOverrides entry of a weave-returned trigger spec in the
// same form as imageKey.
func existingImageKey(spec map[string]any) string {
	list, _ := nested(spec, "imageOverrides").([]any)
	if len(list) == 0 {
		return ""
	}
	return imageKey(firstEntryString(list, "stepName"), firstEntryString(list, "image"), firstEntryString(list, "imagePullPolicy"))
}

// externalAuthFromParams reads an optional weave externalAuthRef from two step params. Both empty
// means "not used" (nil); exactly one set, or a mode other than serviceAccount/oidc, is a permanent
// error. The name is not checked here: weave validates it against its deploy-time allowlist.
func externalAuthFromParams(params map[string]string, modeKey, nameKey string) (map[string]any, error) {
	mode, name := params[modeKey], params[nameKey]
	switch {
	case mode == "" && name == "":
		return nil, nil
	case mode == "" || name == "":
		return nil, permanentf("%s and %s must be set together", modeKey, nameKey)
	case mode != "serviceAccount" && mode != "oidc":
		return nil, permanentf("%s %q is not supported (use serviceAccount or oidc)", modeKey, mode)
	}
	return map[string]any{"mode": mode, "name": name}, nil
}

// ValidateExternalAuthParams checks the externalAuth params of a chain or trigger step (any other
// step type, or params without them, pass). Params that are already resolved are enough, so the API
// can reject a half-set pair at run creation instead of after the build steps have run.
func ValidateExternalAuthParams(typ wizardv1.StepType, params map[string]string) error {
	switch typ {
	case wizardv1.StepChain:
		_, err := externalAuthFromParams(params, "externalAuthMode", "externalAuthName")
		return err
	case wizardv1.StepTrigger:
		_, err := externalAuthFromParams(params, "externalAuthOverrideMode", "externalAuthOverrideName")
		return err
	}
	return nil
}

// authRefKey is the comparable identity of an externalAuthRef ("" when absent).
func authRefKey(ref map[string]any) string {
	if ref == nil {
		return ""
	}
	return fmt.Sprintf("%v/%v", ref["mode"], ref["name"])
}

// authHashParts appends the externalAuthRef identity to a hash's parts only when one is set, so
// hashes of resources created before the feature existed (no ref) stay unchanged.
func authHashParts(ref map[string]any, parts ...string) []string {
	if ref == nil {
		return parts
	}
	return append(parts, "externalAuth", authRefKey(ref))
}

// existingAuthKey is authRefKey for the ref found under field on an existing upstream spec.
func existingAuthKey(spec map[string]any, field string) string {
	mode, name := nestedString(spec, field, "mode"), nestedString(spec, field, "name")
	if mode == "" && name == "" {
		return ""
	}
	return mode + "/" + name
}

func existingPairs(spec map[string]any) []string {
	list, _ := nested(spec, "parameterOverrides").([]any)
	m := map[string]string{}
	for _, item := range list {
		if kv, ok := item.(map[string]any); ok {
			k, _ := kv["name"].(string)
			v, _ := kv["value"].(string)
			m[k] = v
		}
	}
	return sortedPairs(m)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedPairs(m map[string]string) []string {
	var pairs []string
	for _, k := range sortedKeys(m) {
		pairs = append(pairs, k+"="+m[k])
	}
	return pairs
}

// ---- helpers for generic weave JSON ----

func nested(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func nestedString(m map[string]any, path ...string) string {
	s, _ := nested(m, path...).(string)
	return s
}

// resourcesMap renders ResourceRequirements as generic JSON for a weave spec; nil when empty.
func resourcesMap(rr corev1.ResourceRequirements) (map[string]any, error) {
	b, err := json.Marshal(rr)
	if err != nil {
		return nil, fmt.Errorf("encoding default resources: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("encoding default resources: %w", err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}
