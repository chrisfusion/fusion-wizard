// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StepType names one entry of the step catalogue implemented in internal/steps.
// The catalogue has a fixed order (the order of the constants below); a definition
// selects a subset of it and must list its steps in that order.
// +kubebuilder:validation:Enum=gitWatcher;waitBuild;tag;jobTemplate;chain;trigger;batchTrigger
type StepType string

const (
	// StepGitWatcher ensures a forge GitWatcher for the source repository.
	StepGitWatcher StepType = "gitWatcher"
	// StepWaitBuild waits for forge to finish the build and registers the resulting index artifact.
	StepWaitBuild StepType = "waitBuild"
	// StepTag points a fusion-index tag (e.g. "stable") at the built version.
	StepTag StepType = "tag"
	// StepJobTemplate ensures a WeaveJobTemplate that runs the built artifact.
	StepJobTemplate StepType = "jobTemplate"
	// StepChain ensures a WeaveChain that references the job template.
	StepChain StepType = "chain"
	// StepTrigger ensures one or more WeaveTriggers for the chain (supports forEach).
	StepTrigger StepType = "trigger"
	// StepBatchTrigger ensures a single BatchCron WeaveTrigger (many cron-scheduled job entries),
	// created through weave's dedicated /batchtriggers endpoint since the generic trigger endpoint
	// cannot accept an inline job list. Never combined with StepTrigger in the same definition.
	StepBatchTrigger StepType = "batchTrigger"
)

// ParameterType is the value type of a wizard parameter.
// +kubebuilder:validation:Enum=string;number;boolean;stringList
type ParameterType string

const (
	ParameterString     ParameterType = "string"
	ParameterNumber     ParameterType = "number"
	ParameterBoolean    ParameterType = "boolean"
	ParameterStringList ParameterType = "stringList"
)

// WizardParameter declares one user-supplied input of a wizard.
type WizardParameter struct {
	// Name is referenced as ${params.<name>} inside step params.
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// Type of the value; stringList is a JSON array of strings.
	// +kubebuilder:default=string
	// +optional
	Type ParameterType `json:"type,omitempty"`

	// Description is shown by clients that render a form from the definition.
	// +optional
	Description string `json:"description,omitempty"`

	// Required makes a run without this parameter (and without a Default) invalid.
	// +optional
	Required bool `json:"required,omitempty"`

	// Default is used when the run supplies no value. It takes precedence over instance config.
	// +optional
	Default *apiextensionsv1.JSON `json:"default,omitempty"`

	// Pattern is an optional regular expression a string value must match.
	// +optional
	Pattern string `json:"pattern,omitempty"`
}

// WizardStep selects one catalogue step and parameterises it.
type WizardStep struct {
	// Name is unique within the definition and addresses the step's outputs
	// as ${steps.<name>.outputs.<key>}.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Type selects the catalogue step.
	Type StepType `json:"type"`

	// ForEach optionally expands the step once per item of a stringList, e.g. "${params.entrypoints}".
	// Each expansion sees the current item as ${item}.
	// +optional
	ForEach string `json:"forEach,omitempty"`

	// Params are step-specific inputs. Values may contain ${params.x}, ${config.x},
	// ${steps.<step>.outputs.<key>} and ${item} placeholders. Unset keys fall back to instance config.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// WizardDefinitionSpec is an instance-agnostic recipe: which catalogue steps to run and which
// inputs the caller must provide. Per-instance values are never written here; they come from
// the instance config at run time.
type WizardDefinitionSpec struct {
	// Description is a human-readable summary of what the wizard provisions.
	// +optional
	Description string `json:"description,omitempty"`

	// Parameters declares the inputs of the wizard.
	// +listType=map
	// +listMapKey=name
	// +optional
	Parameters []WizardParameter `json:"parameters,omitempty"`

	// Steps is the ordered subset of the step catalogue this wizard runs.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Steps []WizardStep `json:"steps"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=wdef
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WizardDefinition is a reusable, instance-agnostic wizard recipe.
type WizardDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WizardDefinitionSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// WizardDefinitionList contains a list of WizardDefinition.
type WizardDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WizardDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WizardDefinition{}, &WizardDefinitionList{})
}
