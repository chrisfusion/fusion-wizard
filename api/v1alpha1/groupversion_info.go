// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package v1alpha1 contains API Schema definitions for the wizard v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=wizard.fusion-platform.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{Group: "wizard.fusion-platform.io", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
