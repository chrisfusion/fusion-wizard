// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceReference is one run step holding a reference to a managed resource.
type ResourceReference struct {
	// Run is the WizardRun name.
	Run string `json:"run"`

	// Step is the WizardRunStepStatus key within that run.
	Step string `json:"step"`
}

// WizardResourceSpec is one ledger entry: an upstream resource plus every run step that uses it.
// The ledger is the source of truth for rollback — a managed resource is deleted upstream only
// when its last reference is removed.
type WizardResourceSpec struct {
	// Service is the upstream that owns the resource.
	Service ServiceName `json:"service"`

	// Kind is the upstream resource kind, e.g. "gitwatcher", "artifact", "jobtemplate".
	Kind string `json:"kind"`

	// Name is the resource name at the upstream.
	Name string `json:"name"`

	// ExternalID is the upstream's numeric or opaque ID when the name is not enough.
	// +optional
	ExternalID string `json:"externalID,omitempty"`

	// SpecHash identifies the content the resource was created with. A run that wants the
	// same name with a different hash fails with a conflict instead of silently reusing it.
	// +optional
	SpecHash string `json:"specHash,omitempty"`

	// Managed is false for resources found without a ledger entry (e.g. created by hand);
	// they are tracked for visibility but never deleted by a rollback.
	Managed bool `json:"managed"`

	// Terminating is set (with a resourceVersion-guarded update) by the run that removed the last
	// reference, before it deletes the resource upstream. Other runs must not adopt an entry in
	// this state; they wait until it is gone and then create the resource afresh.
	// +optional
	Terminating bool `json:"terminating,omitempty"`

	// Refs lists every run step currently using the resource.
	// +listType=atomic
	// +optional
	Refs []ResourceReference `json:"refs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=wres
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=".spec.service"
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=".spec.kind"
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=".spec.name"
// +kubebuilder:printcolumn:name="Managed",type=boolean,JSONPath=".spec.managed"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WizardResource is a ledger entry tracking one upstream resource created or adopted by wizard runs.
type WizardResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WizardResourceSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// WizardResourceList contains a list of WizardResource.
type WizardResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WizardResource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WizardResource{}, &WizardResourceList{})
}
