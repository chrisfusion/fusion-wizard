// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package envutil reads configuration defaults from environment variables. Both binaries take their
// flags' defaults from the environment so the Helm chart can configure them without extra args.
package envutil

import (
	"os"
	"strings"
)

// String returns the variable's value, or def when it is unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Bool reports whether the variable is "true", "1" or "yes" (case-insensitive).
func Bool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// Split splits a comma-separated value, trimming blanks and dropping empty entries.
func Split(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// List reads a comma-separated variable (see Split).
func List(key string) []string { return Split(os.Getenv(key)) }
