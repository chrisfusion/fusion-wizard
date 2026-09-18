// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package params resolves the ${...} placeholders of wizard definitions and turns caller input
// into typed parameter values. It has no Kubernetes dependency beyond the CRD types.
package params

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RefKind is the namespace of a placeholder reference.
type RefKind string

const (
	RefParams RefKind = "params" // ${params.<name>}
	RefConfig RefKind = "config" // ${config.<key>}
	RefSteps  RefKind = "steps"  // ${steps.<step>.outputs.<key>}
	RefItem   RefKind = "item"   // ${item}, only valid inside a forEach step
)

// Ref is one parsed placeholder.
type Ref struct {
	Kind    RefKind
	Name    string   // parameter name, config key or step name
	Key     string   // output key, RefSteps only
	Filters []string // applied left to right
	Raw     string   // the expression as written, for error messages
}

// Context supplies the values a template is rendered against.
type Context struct {
	Params map[string]any               // typed values from ResolveParameters
	Config map[string]string            // instancecfg.Config.Values()
	Steps  map[string]map[string]string // step name -> outputs of steps that already ran
	Item   *string                      // current forEach item, nil outside forEach
}

// UnresolvedError reports a reference that has no value in the Context.
type UnresolvedError struct {
	Ref    string
	Reason string
}

func (e *UnresolvedError) Error() string {
	return fmt.Sprintf("cannot resolve ${%s}: %s", e.Ref, e.Reason)
}

type segment struct {
	text string
	ref  *Ref
}

// Template is a parsed string with placeholders.
type Template struct {
	segments []segment
}

// Parse splits s into literal text and placeholders. "$${" is an escaped literal "${".
func Parse(s string) (*Template, error) {
	var (
		t   Template
		lit strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			t.segments = append(t.segments, segment{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "$${"):
			lit.WriteString("${")
			i += 3
		case strings.HasPrefix(s[i:], "${"):
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated placeholder in %q", s)
			}
			ref, err := parseRef(s[i+2 : i+2+end])
			if err != nil {
				return nil, err
			}
			flush()
			t.segments = append(t.segments, segment{ref: ref})
			i += 2 + end + 1
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	flush()
	return &t, nil
}

func parseRef(expr string) (*Ref, error) {
	parts := strings.Split(expr, "|")
	path := strings.Split(strings.TrimSpace(parts[0]), ".")
	ref := &Ref{Raw: strings.TrimSpace(expr)}
	switch {
	case len(path) == 1 && path[0] == "item":
		ref.Kind = RefItem
	case len(path) == 2 && path[0] == "params" && path[1] != "":
		ref.Kind, ref.Name = RefParams, path[1]
	case len(path) == 2 && path[0] == "config" && path[1] != "":
		ref.Kind, ref.Name = RefConfig, path[1]
	case len(path) == 4 && path[0] == "steps" && path[2] == "outputs" && path[1] != "" && path[3] != "":
		ref.Kind, ref.Name, ref.Key = RefSteps, path[1], path[3]
	default:
		return nil, fmt.Errorf("invalid reference ${%s}: want params.<name>, config.<key>, steps.<step>.outputs.<key> or item", strings.TrimSpace(expr))
	}
	for _, f := range parts[1:] {
		f = strings.TrimSpace(f)
		if _, ok := filters[f]; !ok {
			return nil, fmt.Errorf("invalid reference ${%s}: unknown filter %q (known: %s)", ref.Raw, f, strings.Join(filterNames(), ", "))
		}
		ref.Filters = append(ref.Filters, f)
	}
	return ref, nil
}

func filterNames() []string {
	names := make([]string, 0, len(filters))
	for n := range filters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Refs returns every placeholder in the template, in order. Definition validation uses it to
// check that referenced parameters and earlier steps exist without rendering anything.
func (t *Template) Refs() []Ref {
	var refs []Ref
	for _, s := range t.segments {
		if s.ref != nil {
			refs = append(refs, *s.ref)
		}
	}
	return refs
}

// Render substitutes every placeholder. A list-valued parameter cannot be rendered into a
// string; it is only usable as a forEach source (see ResolveList).
func (t *Template) Render(c Context) (string, error) {
	var b strings.Builder
	for _, s := range t.segments {
		if s.ref == nil {
			b.WriteString(s.text)
			continue
		}
		v, err := lookup(*s.ref, c)
		if err != nil {
			return "", err
		}
		str, err := stringify(*s.ref, v)
		if err != nil {
			return "", err
		}
		for _, f := range s.ref.Filters {
			str = filters[f](str)
		}
		b.WriteString(str)
	}
	return b.String(), nil
}

func lookup(r Ref, c Context) (any, error) {
	switch r.Kind {
	case RefItem:
		if c.Item == nil {
			return nil, &UnresolvedError{r.Raw, "${item} is only available inside a forEach step"}
		}
		return *c.Item, nil
	case RefParams:
		v, ok := c.Params[r.Name]
		if !ok {
			return nil, &UnresolvedError{r.Raw, fmt.Sprintf("parameter %q has no value", r.Name)}
		}
		return v, nil
	case RefConfig:
		v, ok := c.Config[r.Name]
		if !ok {
			return nil, &UnresolvedError{r.Raw, fmt.Sprintf("instance config has no key %q", r.Name)}
		}
		return v, nil
	case RefSteps:
		outs, ok := c.Steps[r.Name]
		if !ok {
			return nil, &UnresolvedError{r.Raw, fmt.Sprintf("step %q has not produced outputs yet", r.Name)}
		}
		v, ok := outs[r.Key]
		if !ok {
			return nil, &UnresolvedError{r.Raw, fmt.Sprintf("step %q has no output %q", r.Name, r.Key)}
		}
		return v, nil
	}
	return nil, &UnresolvedError{r.Raw, "unsupported reference"}
}

func stringify(r Ref, v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	case []string:
		return "", fmt.Errorf("cannot render ${%s}: list parameters can only be used as forEach", r.Raw)
	}
	return "", fmt.Errorf("cannot render ${%s}: unsupported value type %T", r.Raw, v)
}

// Resolve parses and renders s in one step.
func Resolve(s string, c Context) (string, error) {
	t, err := Parse(s)
	if err != nil {
		return "", err
	}
	return t.Render(c)
}

// ResolveMap resolves every value of m and reports all failures together, keyed by map key.
func ResolveMap(m map[string]string, c Context) (map[string]string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(m))
	var errs []error
	for _, k := range keys {
		v, err := Resolve(m[k], c)
		if err != nil {
			errs = append(errs, fmt.Errorf("param %q: %w", k, err))
			continue
		}
		out[k] = v
	}
	return out, errors.Join(errs...)
}

// ResolveList resolves a forEach source. It must be exactly one ${params.<name>} placeholder
// (no filters, no surrounding text) that refers to a stringList parameter.
func ResolveList(s string, c Context) ([]string, error) {
	t, err := Parse(s)
	if err != nil {
		return nil, err
	}
	if len(t.segments) != 1 || t.segments[0].ref == nil || t.segments[0].ref.Kind != RefParams || len(t.segments[0].ref.Filters) > 0 {
		return nil, fmt.Errorf("forEach %q must be a single ${params.<name>} reference to a stringList parameter", s)
	}
	v, err := lookup(*t.segments[0].ref, c)
	if err != nil {
		return nil, err
	}
	list, ok := v.([]string)
	if !ok {
		return nil, fmt.Errorf("forEach %q: parameter %q is not a stringList", s, t.segments[0].ref.Name)
	}
	return list, nil
}
