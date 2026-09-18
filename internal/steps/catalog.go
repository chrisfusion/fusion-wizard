// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package steps

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"fusion-platform.io/fusion-wizard/internal/instancecfg"
	"fusion-platform.io/fusion-wizard/internal/params"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// catalogueOrder is the fixed order in which steps run. A definition selects a subset and must list
// its steps in this order.
var catalogueOrder = []wizardv1.StepType{
	wizardv1.StepGitWatcher,
	wizardv1.StepWaitBuild,
	wizardv1.StepTag,
	wizardv1.StepJobTemplate,
	wizardv1.StepChain,
	wizardv1.StepTrigger,
}

var registry = func() map[wizardv1.StepType]Step {
	m := map[wizardv1.StepType]Step{}
	for _, s := range []Step{gitWatcherStep{}, waitBuildStep{}, tagStep{}, jobTemplateStep{}, chainStep{}, triggerStep{}} {
		m[s.Type()] = s
	}
	return m
}()

// Lookup returns the catalogue step for a type.
func Lookup(t wizardv1.StepType) (Step, bool) {
	s, ok := registry[t]
	return s, ok
}

func orderIndex(t wizardv1.StepType) int {
	for i, o := range catalogueOrder {
		if o == t {
			return i
		}
	}
	return -1
}

// configKeys are the ${config.x} keys instance config exposes.
var configKeys = func() map[string]struct{} {
	m := map[string]struct{}{}
	for k := range (instancecfg.Config{}).Values() {
		m[k] = struct{}{}
	}
	return m
}()

// ValidateDefinition checks a definition statically, before any run: known step types in catalogue
// order, no unknown or missing step params, and every ${...} reference pointing at something that
// will exist (declared parameters, known config keys, outputs of EARLIER steps, ${item} only under
// forEach). All problems are reported together.
func ValidateDefinition(spec *wizardv1.WizardDefinitionSpec) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	declared := make(map[string]wizardv1.WizardParameter, len(spec.Parameters))
	for _, p := range spec.Parameters {
		if _, dup := declared[p.Name]; dup {
			fail("parameter %q is declared twice", p.Name)
		}
		declared[p.Name] = p
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				fail("parameter %q has an invalid pattern: %v", p.Name, err)
			}
		}
	}
	errs = append(errs, checkDefaults(spec.Parameters)...)

	earlier := map[string]earlierStep{} // steps before the current one, by name
	lastOrder := -1
	for _, st := range spec.Steps {
		step, ok := Lookup(st.Type)
		if !ok {
			fail("step %q has unknown type %q", st.Name, st.Type)
			continue
		}
		if _, dup := earlier[st.Name]; dup {
			fail("step name %q is used twice", st.Name)
		}
		if o := orderIndex(st.Type); o < lastOrder {
			fail("step %q (%s) is out of order; steps must follow %s", st.Name, st.Type, orderString())
		} else {
			lastOrder = o
		}

		ps := step.Params()
		for _, req := range ps.Required {
			if _, ok := st.Params[req]; !ok {
				fail("step %q (%s) is missing required param %q", st.Name, st.Type, req)
			}
		}
		keys := make([]string, 0, len(st.Params))
		for k := range st.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !ps.allows(k) {
				fail("step %q (%s) has unknown param %q", st.Name, st.Type, k)
				continue
			}
			checkRefs(st.Params[k], fmt.Sprintf("step %q param %q", st.Name, k), st.ForEach != "", declared, earlier, fail)
		}

		if st.ForEach != "" {
			checkForEach(st, declared, fail)
		}
		earlier[st.Name] = earlierStep{Step: step, forEach: st.ForEach != ""}
	}
	return errors.Join(errs...)
}

func orderString() string {
	names := make([]string, len(catalogueOrder))
	for i, t := range catalogueOrder {
		names[i] = string(t)
	}
	return strings.Join(names, " -> ")
}

// checkDefaults validates every default value by running it through parameter resolution.
func checkDefaults(defs []wizardv1.WizardParameter) []error {
	relaxed := make([]wizardv1.WizardParameter, len(defs))
	copy(relaxed, defs)
	supplied := map[string]apiextensionsv1.JSON{}
	for i := range relaxed {
		relaxed[i].Required = false
		if d := relaxed[i].Default; d != nil {
			supplied[relaxed[i].Name] = *d
		}
	}
	if _, err := params.ResolveParameters(relaxed, supplied); err != nil {
		return []error{fmt.Errorf("invalid parameter default: %w", err)}
	}
	return nil
}

func checkForEach(st wizardv1.WizardStep, declared map[string]wizardv1.WizardParameter, fail func(string, ...any)) {
	name, err := params.ListRef(st.ForEach)
	if err != nil {
		fail("step %q: %v", st.Name, err)
		return
	}
	p, ok := declared[name]
	switch {
	case !ok:
		fail("step %q forEach refers to undeclared parameter %q", st.Name, name)
	case p.Type != wizardv1.ParameterStringList:
		fail("step %q forEach parameter %q must be of type stringList", st.Name, name)
	}
}

// earlierStep is a step that runs before the one being validated.
type earlierStep struct {
	Step
	forEach bool
}

func checkRefs(value, where string, inForEach bool, declared map[string]wizardv1.WizardParameter,
	earlier map[string]earlierStep, fail func(string, ...any)) {

	t, err := params.Parse(value)
	if err != nil {
		fail("%s: %v", where, err)
		return
	}
	for _, r := range t.Refs() {
		switch r.Kind {
		case params.RefParams:
			p, ok := declared[r.Name]
			switch {
			case !ok:
				fail("%s refers to undeclared parameter %q", where, r.Name)
			case p.Type == wizardv1.ParameterStringList:
				fail("%s: list parameter %q can only be used as forEach", where, r.Name)
			}
		case params.RefConfig:
			if _, ok := configKeys[r.Name]; !ok {
				fail("%s refers to unknown config key %q", where, r.Name)
			}
		case params.RefSteps:
			s, ok := earlier[r.Name]
			switch {
			case !ok:
				fail("%s refers to step %q, which does not exist or does not run before it", where, r.Name)
			case s.forEach:
				fail("%s refers to step %q, which has forEach: it runs several times, so its outputs are ambiguous", where, r.Name)
			case !contains(s.Outputs(), r.Key):
				fail("%s refers to output %q, which step %q (%s) does not provide (has: %s)", where, r.Key, r.Name, s.Type(), strings.Join(s.Outputs(), ", "))
			}
		case params.RefItem:
			if !inForEach {
				fail("%s uses ${item} but the step has no forEach", where)
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
