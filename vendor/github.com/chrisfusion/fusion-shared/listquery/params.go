// SPDX-License-Identifier: GPL-3.0-or-later

package listquery

import (
	"encoding/base64"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Query parameter names shared by every list endpoint.
const (
	ParamQuery    = "q"
	ParamSince    = "since"
	ParamUntil    = "until"
	ParamSource   = "source"
	ParamSort     = "sort"
	ParamLimit    = "limit"
	ParamContinue = "continue"
)

// DefaultMaxLimit caps limit when Spec.MaxLimit is not set.
const DefaultMaxLimit = 500

// maxQueryLen bounds the free-text search term.
const maxQueryLen = 256

// continuePrefix versions the opaque continue token.
const continuePrefix = "o1:"

// labelValueRe matches a Kubernetes label value (source names are label values).
var labelValueRe = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)

// Spec describes what one list endpoint accepts.
type Spec struct {
	// SortKeys are the accepted sort keys; each needs a comparator in Fields.Sort.
	SortKeys []string
	// DefaultSort applies when the request has no sort, e.g. "-created". Empty keeps
	// the storage order.
	DefaultSort string
	// DefaultLimit applies when the request has no limit; 0 returns everything.
	DefaultLimit int
	// MaxLimit is the largest accepted limit; 0 means DefaultMaxLimit.
	MaxLimit int
}

// Params is a parsed list request.
type Params struct {
	// Query is the lower-cased free-text search term; empty matches everything.
	Query string
	// Since and Until bound the time window (Since inclusive, Until exclusive);
	// zero means unbounded.
	Since, Until time.Time
	// Sources keeps items whose Sources contain any of these; empty keeps all.
	Sources []string
	// SortKey and SortDesc select the order; an empty key keeps the input order.
	SortKey  string
	SortDesc bool
	// Limit is the page size (0 = no limit), Offset the start of the page.
	Limit  int
	Offset int
}

// Error is a client error in a query parameter; services answer it with 400.
type Error struct {
	Param string
	Msg   string
}

func (e *Error) Error() string { return e.Param + ": " + e.Msg }

func paramErr(param, msg string) *Error { return &Error{Param: param, Msg: msg} }

// Parse reads the shared parameters from v. now anchors relative times such as
// since=24h. Unknown parameters are ignored so endpoints can add their own.
func Parse(v map[string][]string, spec Spec, now time.Time) (Params, error) {
	var p Params

	if q := strings.TrimSpace(first(v, ParamQuery)); q != "" {
		if len(q) > maxQueryLen {
			return p, paramErr(ParamQuery, "must be at most "+strconv.Itoa(maxQueryLen)+" characters")
		}
		p.Query = strings.ToLower(q)
	}

	var err error
	if p.Since, err = parseTime(ParamSince, first(v, ParamSince), now); err != nil {
		return p, err
	}
	if p.Until, err = parseTime(ParamUntil, first(v, ParamUntil), now); err != nil {
		return p, err
	}
	if !p.Since.IsZero() && !p.Until.IsZero() && !p.Since.Before(p.Until) {
		return p, paramErr(ParamSince, "must be before until")
	}

	for _, s := range Values(v, ParamSource) {
		if !labelValueRe.MatchString(s) {
			return p, paramErr(ParamSource, "invalid value")
		}
		p.Sources = append(p.Sources, s)
	}

	sortRaw := first(v, ParamSort)
	if sortRaw == "" {
		sortRaw = spec.DefaultSort
	}
	if sortRaw != "" {
		p.SortDesc = strings.HasPrefix(sortRaw, "-")
		p.SortKey = strings.TrimPrefix(sortRaw, "-")
		if !slices.Contains(spec.SortKeys, p.SortKey) {
			return p, paramErr(ParamSort, "must be one of "+strings.Join(spec.SortKeys, ", ")+" (prefix - for descending)")
		}
	}

	maxLimit := spec.MaxLimit
	if maxLimit <= 0 {
		maxLimit = DefaultMaxLimit
	}
	p.Limit = spec.DefaultLimit
	if raw := first(v, ParamLimit); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			return p, paramErr(ParamLimit, "must be a number between 1 and "+strconv.Itoa(maxLimit))
		}
		p.Limit = n
	}

	if raw := first(v, ParamContinue); raw != "" {
		off, ok := decodeContinue(raw)
		if !ok {
			return p, paramErr(ParamContinue, "invalid token")
		}
		p.Offset = off
	}
	return p, nil
}

// parseTime accepts an RFC 3339 timestamp or a positive duration back from now
// ("90m", "24h", "7d").
func parseTime(param, raw string, now time.Time) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	// An unencoded "+01:00" offset arrives as " 01:00"; take the space back as "+".
	if t, err := time.Parse(time.RFC3339, strings.ReplaceAll(raw, " ", "+")); err == nil {
		return t, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return time.Time{}, paramErr(param, "must be an RFC 3339 time or a duration like 24h or 7d")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(raw); err != nil {
			return time.Time{}, paramErr(param, "must be an RFC 3339 time or a duration like 24h or 7d")
		}
	}
	if d <= 0 {
		return time.Time{}, paramErr(param, "duration must be positive")
	}
	return now.Add(-d), nil
}

// Values returns the values of key, both repeated (?k=a&k=b) and comma-separated
// (?k=a,b), trimmed, with empty entries dropped.
func Values(v map[string][]string, key string) []string {
	var out []string
	for _, raw := range v[key] {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Set is a set of accepted values; a nil Set matches everything.
type Set map[string]struct{}

// Match reports whether s is in the set, or true when the set is nil.
func (s Set) Match(x string) bool {
	if s == nil {
		return true
	}
	_, ok := s[x]
	return ok
}

// OneOf reads key like Values and checks every value against allowed. It
// returns nil when the parameter is absent.
func OneOf(v map[string][]string, key string, allowed ...string) (Set, error) {
	vals := Values(v, key)
	if len(vals) == 0 {
		return nil, nil
	}
	set := make(Set, len(vals))
	for _, s := range vals {
		if !slices.Contains(allowed, s) {
			return nil, paramErr(key, "must be one of "+strings.Join(allowed, ", "))
		}
		set[s] = struct{}{}
	}
	return set, nil
}

// Any reads key like Values without restricting the values. It returns nil when
// the parameter is absent.
func Any(v map[string][]string, key string) Set {
	vals := Values(v, key)
	if len(vals) == 0 {
		return nil
	}
	set := make(Set, len(vals))
	for _, s := range vals {
		set[s] = struct{}{}
	}
	return set
}

// Bool reads an optional true/false parameter; nil means absent.
func Bool(v map[string][]string, key string) (*bool, error) {
	raw := first(v, key)
	if raw == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, paramErr(key, "must be true or false")
	}
	return &b, nil
}

func first(v map[string][]string, key string) string {
	if vals := v[key]; len(vals) > 0 {
		return strings.TrimSpace(vals[0])
	}
	return ""
}

func encodeContinue(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(continuePrefix + strconv.Itoa(offset)))
}

func decodeContinue(tok string) (int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(b), continuePrefix))
	if err != nil || n < 0 || !strings.HasPrefix(string(b), continuePrefix) {
		return 0, false
	}
	return n, true
}
