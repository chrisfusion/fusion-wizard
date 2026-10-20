// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"fusion-platform.io/fusion-wizard/internal/ledger"
	"fusion-platform.io/fusion-wizard/internal/params"
	"fusion-platform.io/fusion-wizard/internal/upstream"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// App types and the names the step fixes for the single things weave would otherwise take from the
// caller.
const (
	appTypeService = "service"
	appTypeJob     = "job"

	// appFireAnnotation makes weave launch the named entrypoint of a job app once (consumed by it).
	appFireAnnotation = "fusion-platform.io/fire"
	// defaultEntrypoint names the only entrypoint of a job app that is given no entrypoints list.
	defaultEntrypoint = "main"
	// maxEntrypointName is WeaveAppEntrypoint.name's length limit.
	maxEntrypointName = 40
	// entrypointEnv is the env var the Python runner reads its entrypoint file from.
	entrypointEnv = "ENTRYPOINT"
)

// Pending marks a step param that cannot be resolved yet (it depends on an earlier step's output
// or on instance config). ValidateAppParams skips every check that involves such a value.
const Pending = "\x00pending"

type appStep struct{}

func (appStep) Type() wizardv1.StepType { return wizardv1.StepApp }
func (appStep) Outputs() []string       { return []string{"name"} }
func (appStep) Params() ParamSpec {
	return ParamSpec{
		Required: []string{"name", "type"},
		Optional: []string{"image", "artifactName", "tag", "port", "ingressName", "schedule", "entrypoints", "fireOnCreate", "externalAuthMode", "externalAuthName"},
	}
}

// appEntrypoint is one entry of the entrypoints param: the objectList shape python-git-job takes.
type appEntrypoint struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Schedule string `json:"schedule"`
}

// name is the WeaveApp entrypoint name for a file key: "etl/load.py" -> "etl-load".
func (e appEntrypoint) name() string {
	n := params.K8sName(params.Stem(e.Key))
	if len(n) > maxEntrypointName {
		n = strings.TrimRight(n[:maxEntrypointName], "-")
	}
	return n
}

// Ensure creates one WeaveApp, the single object that replaces the template + chain + run/trigger
// objects for the one-step case: it owns its Deployment/Service/Ingress (service) or Jobs (job), so
// deleting it is the whole teardown.
//
// Mode follows artifactName/tag (both or neither): with them the app runs the built artifact on the
// instance's runner image (codeSource); without them it runs the caller's image, which must have an
// explicit non-latest tag or a digest. Weave's ALLOWED_IMAGE_PREFIXES stays the policy gate.
//
// A job app has either an entrypoints list (objectList JSON: key = file, type OnDemand|Cron,
// schedule for Cron; each runs with ENTRYPOINT=<key>) or, without one, the single entrypoint "main"
// that runs on `schedule` (empty = on demand). fireOnCreate: "true" launches that single entrypoint
// once, through the fire annotation on the create call, so it only ever happens when the app is
// really created (never on adopt/re-confirm).
//
// externalAuthMode/externalAuthName (both or neither) set the app's externalAuthRef; weave mints a
// token per job execution, so a service has none.
func (appStep) Ensure(ctx context.Context, env *Env, in Input) (Result, error) {
	if err := ValidateAppParams(wizardv1.StepApp, in.Params); err != nil {
		return Result{}, err
	}
	name, typ := in.Params["name"], in.Params["type"]
	artifactName, tag := in.Params["artifactName"], in.Params["tag"]
	codeSource := artifactName != ""
	image := in.Params["image"]
	if codeSource {
		image = firstNonEmpty(image, env.Cfg.RunnerImage)
	}

	spec := map[string]any{"type": typ, "image": image}
	// The runner image of a codeSource app is an instance default, so like a template it is not part
	// of the identity; a caller's image is.
	hashParts := []string{KindApp, typ, artifactName, tag}
	if !codeSource {
		hashParts = append(hashParts, "image", image)
	} else {
		spec["codeSource"] = map[string]any{"artifactName": artifactName, "tag": tag}
	}
	res, err := resourcesMap(env.Cfg.DefaultResources)
	if err != nil {
		return Result{}, err
	}
	if res != nil {
		spec["resources"] = res
	}

	port, ingressName := in.Params["port"], in.Params["ingressName"]
	var eps []map[string]any
	authRef, err := externalAuthFromParams(in.Params, "externalAuthMode", "externalAuthName")
	if err != nil {
		return Result{}, err
	}

	if typ == appTypeService {
		service := map[string]any{}
		if port != "" {
			n, _ := strconv.Atoi(port)
			service["ports"] = []any{map[string]any{"name": servicePortName, "port": n}}
		}
		if ingressName != "" {
			service["ingressName"] = ingressName
		}
		if len(service) > 0 {
			spec["service"] = service
		}
		hashParts = append(hashParts, port, ingressName)
	} else {
		eps, err = appEntrypoints(in.Params)
		if err != nil {
			return Result{}, err
		}
		list := make([]any, len(eps))
		for i, ep := range eps {
			list[i] = ep
			hashParts = append(hashParts, appEntrypointKey(ep))
		}
		spec["entrypoints"] = list
		if authRef != nil {
			spec["externalAuthRef"] = authRef
		}
	}

	obj := upstream.NewWeaveObject("WeaveApp", name, spec)
	if in.Params["fireOnCreate"] == "true" {
		obj["metadata"].(map[string]any)["annotations"] = map[string]any{appFireAnnotation: eps[0]["name"]}
	}

	return env.weaveResource(ctx, in, KindApp, name, ledger.Hash(authHashParts(authRef, hashParts...)...), obj,
		func(existing map[string]any) string {
			switch {
			case nestedString(existing, "type") != typ:
				return "it is a different type of app"
			case nestedString(existing, "codeSource", "artifactName") != artifactName || nestedString(existing, "codeSource", "tag") != tag:
				return "it runs a different artifact or tag"
			case !codeSource && nestedString(existing, "image") != image:
				return "it runs a different image"
			case typ == appTypeService && existingAppPort(existing) != port:
				return "it exposes a different port"
			case typ == appTypeService && nestedString(existing, "service", "ingressName") != ingressName:
				return "it has a different ingress"
			case typ == appTypeJob && strings.Join(existingAppEntrypointKeys(existing), "\x00") != strings.Join(appEntrypointKeys(eps), "\x00"):
				return "it has different entrypoints"
			case typ == appTypeJob && existingAuthKey(existing, "externalAuthRef") != authRefKey(authRef):
				return "it has a different externalAuthRef"
			}
			return ""
		}, nil, map[string]string{"name": name})
}

// appEntrypoints builds the entrypoints of a job app from the params (already validated).
func appEntrypoints(p map[string]string) ([]map[string]any, error) {
	if p["entrypoints"] == "" {
		ep := map[string]any{"name": defaultEntrypoint}
		if p["schedule"] != "" {
			ep["schedule"] = p["schedule"]
		}
		return []map[string]any{ep}, nil
	}
	list, err := parseEntrypoints(p["entrypoints"])
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(list))
	for i, e := range list {
		ep := map[string]any{
			"name": e.name(),
			"env":  []any{map[string]any{"name": entrypointEnv, "value": e.Key}},
		}
		if e.Schedule != "" {
			ep["schedule"] = e.Schedule
		}
		out[i] = ep
	}
	return out, nil
}

func parseEntrypoints(raw string) ([]appEntrypoint, error) {
	var list []appEntrypoint
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, permanentf("entrypoints is not a JSON list of objects: %v", err)
	}
	return list, nil
}

// appEntrypointKey is the comparable identity of one entrypoint: name, schedule and ENTRYPOINT value.
func appEntrypointKey(ep map[string]any) string {
	var value string
	if env, _ := ep["env"].([]any); len(env) > 0 {
		m, _ := env[0].(map[string]any)
		value, _ = m["value"].(string)
	}
	name, _ := ep["name"].(string)
	schedule, _ := ep["schedule"].(string)
	return name + "|" + schedule + "|" + value
}

func appEntrypointKeys(eps []map[string]any) []string {
	keys := make([]string, len(eps))
	for i, ep := range eps {
		keys[i] = appEntrypointKey(ep)
	}
	sort.Strings(keys)
	return keys
}

// existingAppEntrypointKeys reads the entrypoints back out of a weave-returned spec in the same form.
func existingAppEntrypointKeys(spec map[string]any) []string {
	list, _ := nested(spec, "entrypoints").([]any)
	var eps []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			eps = append(eps, m)
		}
	}
	return appEntrypointKeys(eps)
}

// existingAppPort reads the first service port's number back out of a weave-returned app spec ("" when
// none), accepting the typed value the in-memory fakes keep as well as real weave's float64.
func existingAppPort(spec map[string]any) string {
	return existingPort(map[string]any{"ports": nested(spec, "service", "ports")})
}

// ValidateAppParams checks the params of an app step (any other step type passes). Params that are
// already resolved are enough, so the API can reject a bad app at run creation instead of after the
// build steps have run; a Pending value skips the checks that need it.
func ValidateAppParams(typ wizardv1.StepType, p map[string]string) error {
	if typ != wizardv1.StepApp {
		return nil
	}
	known := func(k string) bool { return p[k] != Pending }
	appType := p["type"]
	switch {
	case !known("type"):
		return nil
	case appType != appTypeService && appType != appTypeJob:
		return permanentf("app type %q is not supported (use service or job)", appType)
	}

	if known("artifactName") && known("tag") {
		switch {
		case (p["artifactName"] == "") != (p["tag"] == ""):
			return permanentf("app needs artifactName and tag together, or neither (image only)")
		case p["artifactName"] == "" && known("image"):
			if err := ValidateImageParams(wizardv1.StepRun, map[string]string{"image": p["image"]}); err != nil {
				return err
			}
		}
	}

	if known("externalAuthMode") && known("externalAuthName") {
		ref, err := externalAuthFromParams(p, "externalAuthMode", "externalAuthName")
		if err != nil {
			return err
		}
		if ref != nil && appType == appTypeService {
			return permanentf("externalAuth is not supported for a service app (its token would expire under a long-running deployment)")
		}
	}

	if appType == appTypeService {
		for _, k := range []string{"entrypoints", "schedule", "fireOnCreate"} {
			if known(k) && p[k] != "" {
				return permanentf("%s is only valid for a job app", k)
			}
		}
		if known("port") && p["port"] != "" {
			if n, err := strconv.Atoi(p["port"]); err != nil || n < 1 || n > 65535 {
				return permanentf("port %q is not a valid port number", p["port"])
			}
		}
		return nil
	}

	for _, k := range []string{"port", "ingressName"} {
		if known(k) && p[k] != "" {
			return permanentf("%s is only valid for a service app", k)
		}
	}
	if !known("entrypoints") || !known("schedule") || !known("fireOnCreate") {
		return nil
	}
	if p["entrypoints"] == "" {
		return nil // the single "main" entrypoint: any schedule (weave validates the expression)
	}
	if p["schedule"] != "" || p["fireOnCreate"] == "true" {
		return permanentf("schedule and fireOnCreate are only valid without an entrypoints list")
	}
	list, err := parseEntrypoints(p["entrypoints"])
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return permanentf("entrypoints must not be empty")
	}
	seen := map[string]string{}
	for _, e := range list {
		switch {
		case e.Type != "" && e.Type != "OnDemand" && e.Type != "Cron":
			return permanentf("entrypoint %q: type %q is not supported (use OnDemand or Cron)", e.Key, e.Type)
		case e.Type == "Cron" && e.Schedule == "":
			return permanentf("entrypoint %q: a Cron entrypoint needs a schedule", e.Key)
		case e.Type != "Cron" && e.Schedule != "":
			return permanentf("entrypoint %q: schedule is only valid for a Cron entrypoint", e.Key)
		}
		if other, dup := seen[e.name()]; dup {
			return permanentf("entrypoints %q and %q both map to the name %q", other, e.Key, e.name())
		}
		seen[e.name()] = e.Key
	}
	return nil
}
