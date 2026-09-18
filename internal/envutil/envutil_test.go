// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package envutil

import (
	"reflect"
	"testing"
)

func TestString(t *testing.T) {
	t.Setenv("ENVUTIL_S", "value")
	t.Setenv("ENVUTIL_EMPTY", "")
	if String("ENVUTIL_S", "d") != "value" || String("ENVUTIL_EMPTY", "d") != "d" || String("ENVUTIL_UNSET", "d") != "d" {
		t.Error("String must fall back to the default for unset or empty variables")
	}
}

func TestBool(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "yes": true, "false": false, "0": false, "": false, "maybe": false} {
		t.Setenv("ENVUTIL_B", v)
		if got := Bool("ENVUTIL_B"); got != want {
			t.Errorf("Bool(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestSplit(t *testing.T) {
	if got, want := Split(" a, ,b ,"), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Split = %v, want %v", got, want)
	}
	if Split("") != nil || Split(" , ") != nil {
		t.Error("nothing to split must give a nil list")
	}
}

func TestList(t *testing.T) {
	t.Setenv("ENVUTIL_L", " fusion/bff , ,fusion/ext-bff,")
	if got, want := List("ENVUTIL_L"), []string{"fusion/bff", "fusion/ext-bff"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if got := List("ENVUTIL_UNSET"); got != nil {
		t.Errorf("an unset variable must give a nil list, got %v", got)
	}
}
