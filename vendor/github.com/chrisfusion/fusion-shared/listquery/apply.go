// SPDX-License-Identifier: GPL-3.0-or-later

package listquery

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Fields tells the generic functions how to read an item. Name is required;
// Time is required when a time window can be requested, Labels when a source
// can be requested, and Sort must hold a comparator for every Spec.SortKeys entry.
type Fields[T any] struct {
	Name   func(*T) string
	Time   func(*T) time.Time
	Labels func(*T) map[string]string
	// Sort maps a sort key to an ascending comparator; ties are broken by Name.
	Sort map[string]func(a, b *T) int
}

// Page is one page of a filtered, sorted list.
type Page[T any] struct {
	Items []T
	// Total counts all matching items, before pagination.
	Total int
	// Continue fetches the next page; empty on the last page.
	Continue string
	// Remaining counts the matching items after this page.
	Remaining int
}

// Apply filters, sorts and paginates items. extra adds endpoint-specific
// conditions and may be nil. The backing array of items is reused.
func Apply[T any](items []T, p Params, f Fields[T], extra func(*T) bool) Page[T] {
	items = Filter(items, p, f, extra)
	Sort(items, p, f)
	return Paginate(items, p)
}

// Filter keeps the items that match the search term, time window, sources and
// extra (which may be nil). The backing array of items is reused.
func Filter[T any](items []T, p Params, f Fields[T], extra func(*T) bool) []T {
	out := items[:0]
	for i := range items {
		it := &items[i]
		if p.Query != "" && !strings.Contains(strings.ToLower(f.Name(it)), p.Query) {
			continue
		}
		if !p.Since.IsZero() || !p.Until.IsZero() {
			t := f.Time(it)
			if t.IsZero() || (!p.Since.IsZero() && t.Before(p.Since)) || (!p.Until.IsZero() && !t.Before(p.Until)) {
				continue
			}
		}
		if len(p.Sources) > 0 && !anyOf(Sources(f.Labels(it)), p.Sources) {
			continue
		}
		if extra != nil && !extra(it) {
			continue
		}
		out = append(out, *it)
	}
	clear(items[len(out):])
	return out
}

// Sort orders items by p.SortKey, ties broken by name ascending. It panics when
// the key has no comparator in f.Sort (a programming error: Spec and Fields disagree).
func Sort[T any](items []T, p Params, f Fields[T]) {
	if p.SortKey == "" {
		return
	}
	less, ok := f.Sort[p.SortKey]
	if !ok {
		panic("listquery: no comparator for sort key " + p.SortKey)
	}
	slices.SortStableFunc(items, func(a, b T) int {
		c := less(&a, &b)
		if p.SortDesc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return cmp.Compare(f.Name(&a), f.Name(&b))
	})
}

// Paginate cuts the page selected by p.Offset and p.Limit out of items.
func Paginate[T any](items []T, p Params) Page[T] {
	page := Page[T]{Total: len(items)}
	start := min(p.Offset, len(items))
	end := len(items)
	if p.Limit > 0 && start+p.Limit < end {
		end = start + p.Limit
		page.Continue = encodeContinue(end)
	}
	page.Items = items[start:end]
	page.Remaining = len(items) - end
	return page
}

// Count tallies the values key returns for each item, e.g. phase or chain
// counts for the filter drop-downs of a GUI. Empty values are skipped.
func Count[T any](items []T, key func(*T) []string) map[string]int {
	out := map[string]int{}
	for i := range items {
		for _, k := range key(&items[i]) {
			if k != "" {
				out[k]++
			}
		}
	}
	return out
}

// CompareTime orders times ascending, the zero time first.
func CompareTime(a, b time.Time) int { return a.Compare(b) }

func anyOf(have, want []string) bool {
	for _, w := range want {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}
