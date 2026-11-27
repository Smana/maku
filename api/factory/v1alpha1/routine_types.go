// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/internal/factory/config"
)

// RoutineSpec is a Routine's reusable upkeep definition: config.RoutineSpec.
type RoutineSpec struct {
	config.RoutineSpec `json:",inline"`
}

// RoutineStatus is what the factory made of its Routine: Accepted, counts, and consumers.
type RoutineStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ConsumerCount, ActiveCount, and PausedCount are always serialized, zero included: the
	// printer columns read them.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ConsumerCount int `json:"consumerCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ActiveCount int `json:"activeCount"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	PausedCount int `json:"pausedCount"`
	// Consumers are the Repositories that bind this Routine.
	// +listType=map
	// +listMapKey=repository
	// +kubebuilder:validation:MaxItems=64
	Consumers []RoutineConsumerStatus `json:"consumers,omitempty"`
}

// RoutineConsumerStatus is one Repository's binding of a Routine.
type RoutineConsumerStatus struct {
	// Repository is the Repository object's name.
	// +kubebuilder:validation:MaxLength=253
	Repository string `json:"repository"`
	// +kubebuilder:validation:Enum=propose;draft;pr;disabled
	Mode             string `json:"mode,omitempty"`
	ServedGeneration int64  `json:"servedGeneration,omitempty"`
	// Accepted is always serialized, false included.
	// +optional
	Accepted bool `json:"accepted"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// Paused records whether this consumer is currently paused operationally or by breaker.
	// +optional
	Paused bool `json:"paused,omitempty"`
}

// Routine is one reusable upkeep routine definition, bound by Repositories.
//
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$')",message="a Routine's name must match ^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name=Consumers,type=integer,JSONPath=`.status.consumerCount`
// +kubebuilder:printcolumn:name=Active,type=integer,JSONPath=`.status.activeCount`
// +kubebuilder:printcolumn:name=Paused,type=integer,JSONPath=`.status.pausedCount`
// +kubebuilder:printcolumn:name=Accepted,type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
type Routine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RoutineSpec   `json:"spec"`
	Status            RoutineStatus `json:"status,omitempty"`
}

// RoutineList is a list of Routine.
//
// +kubebuilder:object:root=true
type RoutineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Routine `json:"items"`
}

// RoutineBindingStatus is one Routine binding on a Repository, with its resolution and yield state.
type RoutineBindingStatus struct {
	// +kubebuilder:validation:MaxLength=40
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=propose;draft;pr;disabled
	Mode              string `json:"mode,omitempty"`
	RoutineGeneration int64  `json:"routineGeneration,omitempty"`
	// Accepted is always serialized, false included.
	// +optional
	Accepted bool `json:"accepted"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	PromptSHA string `json:"promptSHA,omitempty"`
	// +kubebuilder:validation:Enum=ok;half_open;paused
	BreakerState string `json:"breakerState,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	BreakerReason string       `json:"breakerReason,omitempty"`
	LastRunAt     *metav1.Time `json:"lastRunAt,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	LastOutcome string `json:"lastOutcome,omitempty"`
	// +kubebuilder:validation:Minimum=0
	OpenCount int `json:"openCount,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RollingMerged int `json:"rollingMerged,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RollingClosed int `json:"rollingClosed,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RollingNoOp int `json:"rollingNoOp,omitempty"`
}
