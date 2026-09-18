// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package params

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	wizardv1 "fusion-platform.io/fusion-wizard/api/v1alpha1"
)

// ResolveParameters validates the caller's input against the definition's parameter schema and
// returns typed values: string, float64, bool or []string. A value the caller omits falls back to
// the definition default; instance config is a separate ${config.x} namespace and is not merged
// here. All problems are reported together so a client can fix its form in one round trip.
func ResolveParameters(defs []wizardv1.WizardParameter, supplied map[string]apiextensionsv1.JSON) (map[string]any, error) {
	var errs []error

	known := make(map[string]struct{}, len(defs))
	for _, d := range defs {
		known[d.Name] = struct{}{}
	}
	var unknown []string
	for name := range supplied {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errs = append(errs, fmt.Errorf("unknown parameter(s): %s", strings.Join(unknown, ", ")))
	}

	out := make(map[string]any, len(defs))
	for _, d := range defs {
		raw, ok := supplied[d.Name]
		if !ok || isNull(raw.Raw) {
			if d.Default == nil || isNull(d.Default.Raw) {
				if d.Required {
					errs = append(errs, fmt.Errorf("parameter %q is required", d.Name))
				}
				continue
			}
			raw = *d.Default
		}
		v, err := decode(d, raw.Raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("parameter %q: %w", d.Name, err))
			continue
		}
		out[d.Name] = v
	}
	return out, errors.Join(errs...)
}

func isNull(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func decode(d wizardv1.WizardParameter, raw []byte) (any, error) {
	var pattern *regexp.Regexp
	if d.Pattern != "" {
		p, err := regexp.Compile(d.Pattern)
		if err != nil {
			return nil, fmt.Errorf("definition has an invalid pattern: %w", err)
		}
		pattern = p
	}
	match := func(s string) error {
		if pattern != nil && !pattern.MatchString(s) {
			return fmt.Errorf("%q does not match pattern %s", s, d.Pattern)
		}
		return nil
	}

	switch d.Type {
	case wizardv1.ParameterNumber:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, errors.New("expected a number")
		}
		return n, nil
	case wizardv1.ParameterBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, errors.New("expected a boolean")
		}
		return b, nil
	case wizardv1.ParameterStringList:
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, errors.New("expected a list of strings")
		}
		if d.Required && len(list) == 0 {
			return nil, errors.New("must not be empty")
		}
		for _, item := range list {
			if err := match(item); err != nil {
				return nil, err
			}
		}
		return list, nil
	default: // string, including an unset type (the CRD defaults it, API-built objects may not)
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errors.New("expected a string")
		}
		if d.Required && s == "" {
			return nil, errors.New("must not be empty")
		}
		if err := match(s); err != nil {
			return nil, err
		}
		return s, nil
	}
}
