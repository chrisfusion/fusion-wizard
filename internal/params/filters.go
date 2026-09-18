// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package params

import (
	"regexp"
	"strings"
)

const maxK8sName = 63

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// filters are the pipe filters usable in a placeholder, e.g. ${item|stem|k8sName}.
var filters = map[string]func(string) string{
	"lower":   strings.ToLower,
	"stem":    stem,
	"k8sName": k8sName,
}

// K8sName turns an arbitrary string into a DNS-label-safe name (lowercase alphanumerics and
// dashes, at most 63 characters). It mirrors toK8sName in spectra's useGitAppProvisioning.ts so
// names derived by the wizard match the ones the browser wizards produce.
func K8sName(s string) string { return k8sName(s) }

func k8sName(s string) string {
	name := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if name == "" {
		return "x"
	}
	if len(name) > maxK8sName {
		name = strings.TrimRight(name[:maxK8sName], "-")
	}
	return name
}

// stem drops the final file extension: "main.py" -> "main". A leading dot is not an extension
// (".hidden" stays), and dots inside directory components are ignored.
func stem(s string) string {
	i := strings.LastIndex(s, ".")
	if i <= 0 || strings.Contains(s[i:], "/") {
		return s
	}
	return s[:i]
}
