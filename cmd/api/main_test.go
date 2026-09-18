// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]struct {
		level slog.Level
		ok    bool
	}{
		"debug": {slog.LevelDebug, true}, "INFO": {slog.LevelInfo, true}, "": {slog.LevelInfo, true},
		"warn": {slog.LevelWarn, true}, "warning": {slog.LevelWarn, true}, "error": {slog.LevelError, true},
		"verbose": {slog.LevelInfo, false},
	}
	for in, want := range cases {
		if level, ok := parseLevel(in); level != want.level || ok != want.ok {
			t.Errorf("parseLevel(%q) = %v, %v; want %v, %v", in, level, ok, want.level, want.ok)
		}
	}
}
