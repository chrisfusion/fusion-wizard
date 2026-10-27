// SPDX-License-Identifier: GPL-3.0-or-later

// Package listquery parses and applies the list query parameters that fusion
// services share, so every list endpoint filters, sorts and pages the same way:
//
//	q         case-insensitive substring of the name
//	since     start of the time window (RFC 3339, or a duration back from now: 24h, 7d)
//	until     end of the time window, exclusive (same formats)
//	source    where the item came from: gitops (applied by Flux) or a managed-by value
//	sort      a sort key, prefixed with - for descending
//	limit     page size
//	continue  opaque token from the previous page
//
// Repeated and comma-separated values are equivalent (source=a,b or
// source=a&source=b). Filtering happens in memory, before pagination, so pages and
// totals are consistent; endpoints add their own conditions through the extra
// argument of Apply and parse them with OneOf, Any and Bool.
//
//	p, err := listquery.Parse(r.URL.Query(), spec, time.Now())
//	if err != nil { /* 400 with err.Error() */ }
//	page := listquery.Apply(items, p, fields, func(it *Item) bool { return phases.Match(it.Phase) })
package listquery
