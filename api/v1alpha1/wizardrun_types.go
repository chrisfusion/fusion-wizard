// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunDesiredState is what the caller wants the run to converge to.
// +kubebuilder:validation:Enum=Applied;RolledBack
type RunDesiredState string

const (
	// DesiredApplied provisions every step of the definition.
	DesiredApplied RunDesiredState = "Applied"
	// DesiredRolledBack tears down everything this run owns; shared resources survive
	// until the last run referencing them is rolled back.
	DesiredRolledBack RunDesiredState = "RolledBack"
)

// RunPhase is the overall state of a WizardRun.
// +kubebuilder:validation:Enum=Pending;Running;Ready;Failed;RollingBack;RolledBack;RollbackFailed
type RunPhase string

const (
	RunPending        RunPhase = "Pending"
	RunRunning        RunPhase = "Running"
	RunReady          RunPhase = "Ready"
	RunFailed         RunPhase = "Failed"
	RunRollingBack    RunPhase = "RollingBack"
	RunRolledBack     RunPhase = "RolledBack"
	RunRollbackFailed RunPhase = "RollbackFailed"
)

// StepPhase is the state of one (possibly forEach-expanded) step.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;RolledBack;RollbackFailed
type StepPhase string

const (
	StepPending        StepPhase = "Pending"
	StepRunning        StepPhase = "Running"
	StepSucceeded      StepPhase = "Succeeded"
	StepFailed         StepPhase = "Failed"
	StepRolledBack     StepPhase = "RolledBack"
	StepRollbackFailed StepPhase = "RollbackFailed"
)

// ServiceName identifies the upstream service that owns a managed resource.
// +kubebuilder:validation:Enum=forge;index;weave
type ServiceName string

const (
	ServiceForge ServiceName = "forge"
	ServiceIndex ServiceName = "index"
	ServiceWeave ServiceName = "weave"
)

// Disposition records how a step obtained a resource. It decides what rollback may do with it.
// +kubebuilder:validation:Enum=Created;Adopted;FoundUnowned
type Disposition string

const (
	// DispositionCreated: this step created the resource and registered it in the ledger.
	DispositionCreated Disposition = "Created"
	// DispositionAdopted: the resource already existed in the ledger (another run created it);
	// this run added a reference to it.
	DispositionAdopted Disposition = "Adopted"
	// DispositionFoundUnowned: the resource existed without a ledger entry (e.g. made by hand);
	// it is recorded as unmanaged and never deleted by a rollback.
	DispositionFoundUnowned Disposition = "FoundUnowned"
)

const (
	// ConditionReady is the condition type reported on a WizardRun.
	ConditionReady = "Ready"

	// FinalizerRollback keeps a WizardRun alive until everything it provisioned was rolled back.
	FinalizerRollback = "wizard.fusion-platform.io/rollback"

	// AnnotationRetry is a one-shot request to retry a Failed run: the controller resets the failed
	// steps and removes the annotation.
	AnnotationRetry = "wizard.fusion-platform.io/retry"
)

// ManagedResourceRef points a run step at one upstream resource.
type ManagedResourceRef struct {
	// Service is the upstream that owns the resource.
	Service ServiceName `json:"service"`

	// Kind is the upstream resource kind, e.g. "gitwatcher", "artifact", "jobtemplate".
	Kind string `json:"kind"`

	// Name is the resource name at the upstream.
	Name string `json:"name"`

	// ExternalID is the upstream's numeric or opaque ID when the name is not enough (e.g. index artifact ID).
	// +optional
	ExternalID string `json:"externalID,omitempty"`

	// Disposition is how this run obtained the resource.
	Disposition Disposition `json:"disposition"`
}

// WizardRunSpec is one execution of a WizardDefinition.
type WizardRunSpec struct {
	// DefinitionRef names the WizardDefinition in the same namespace.
	DefinitionRef corev1.LocalObjectReference `json:"definitionRef"`

	// DefinitionSnapshot is a copy of the definition taken when the run starts, so editing
	// the definition later never changes a running run or its rollback. The API server fills it;
	// the controller fills it on first reconcile for runs created with kubectl.
	// +optional
	DefinitionSnapshot *WizardDefinitionSpec `json:"definitionSnapshot,omitempty"`

	// DefinitionGeneration is the metadata.generation of the definition the snapshot was taken from.
	// +optional
	DefinitionGeneration int64 `json:"definitionGeneration,omitempty"`

	// Parameters are the caller's inputs, keyed by parameter name. Scalars are JSON strings,
	// numbers or booleans; stringList values are JSON arrays of strings.
	// +optional
	Parameters map[string]apiextensionsv1.JSON `json:"parameters,omitempty"`

	// CreatedBy is the user ID forwarded by a trusted BFF (X-User-ID). Stamped once at creation.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="createdBy is immutable"
	// +optional
	CreatedBy string `json:"createdBy,omitempty"`

	// CreatedByEmail is the user email forwarded by a trusted BFF (X-User-Email).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="createdByEmail is immutable"
	// +optional
	CreatedByEmail string `json:"createdByEmail,omitempty"`

	// DesiredState is Applied (default) or RolledBack.
	// +kubebuilder:default=Applied
	// +optional
	DesiredState RunDesiredState `json:"desiredState,omitempty"`
}

// WizardRunStepStatus reports one step instance. A step with forEach yields one entry per item.
type WizardRunStepStatus struct {
	// Key is unique within the run: the step name, or "<name>/<item>" for forEach expansions.
	Key string `json:"key"`

	// Name is the step name from the definition.
	Name string `json:"name"`

	// Type is the catalogue step type.
	// +optional
	Type StepType `json:"type,omitempty"`

	// Item is the forEach item this instance was expanded for.
	// +optional
	Item string `json:"item,omitempty"`

	// Phase is the current state of the step instance.
	// +optional
	Phase StepPhase `json:"phase,omitempty"`

	// Message carries the last error or progress detail.
	// +optional
	Message string `json:"message,omitempty"`

	// Failures counts consecutive transient failures; the step fails for good after too many.
	// +optional
	Failures int32 `json:"failures,omitempty"`

	// Outputs are values later steps can reference as ${steps.<name>.outputs.<key>}.
	// +optional
	Outputs map[string]string `json:"outputs,omitempty"`

	// Resources are the upstream resources this step instance holds a ledger reference to.
	// +optional
	Resources []ManagedResourceRef `json:"resources,omitempty"`

	// StartedAt is when the step first ran; waitBuild measures its timeout from here.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the step reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// WizardRunStatus is the observed state of a WizardRun.
type WizardRunStatus struct {
	// Phase is the overall run state.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// Message is a human-readable summary of the phase, e.g. the failing step's error.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the spec generation the controller last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Steps reports every step instance in execution order.
	// +listType=map
	// +listMapKey=key
	// +optional
	Steps []WizardRunStepStatus `json:"steps,omitempty"`

	// Conditions follow the standard Kubernetes condition convention; Ready is the only one used.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// StartedAt is when the run first started applying.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the run reached Ready, Failed, RolledBack or RollbackFailed.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=wrun
// +kubebuilder:printcolumn:name="Definition",type=string,JSONPath=".spec.definitionRef.name"
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=".spec.desiredState"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="CreatedBy",type=string,JSONPath=".spec.createdBy"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WizardRun is one persistent, observable execution of a WizardDefinition.
type WizardRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WizardRunSpec   `json:"spec,omitempty"`
	Status WizardRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WizardRunList contains a list of WizardRun.
type WizardRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WizardRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WizardRun{}, &WizardRunList{})
}
