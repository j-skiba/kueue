/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DynamicQuotaOrchestratorActive      = "Active"
	DynamicQuotaOrchestratorDistributed = "Distributed"

	DynamicQuotaOrchestratorReasonActive                               = "Active"
	DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator  = "ConflictingDynamicQuotaOrchestrator"
	DynamicQuotaOrchestratorReasonTooManyFlavorsInAggregatedCapacity   = "TooManyFlavorsInAggregatedCapacity"
	DynamicQuotaOrchestratorReasonTooManyResourcesInAggregatedCapacity = "TooManyResourcesInAggregatedCapacity"
	DynamicQuotaOrchestratorReasonQuotaDistributed                     = "QuotaDistributed"
	DynamicQuotaOrchestratorReasonSubtreeRootNotFound                  = "SubtreeRootNotFound"
)

type DynamicQuotaOrchestratorSpec struct {
	// capacityDiscovery specifies capacity aggregation.
	//
	// +required
	CapacityDiscovery CapacityDiscovery `json:"capacityDiscovery"`

	// capacityDistribution specifies how aggregated capacity is distributed.
	// When omitted, the DQO is discovery-only: it reports aggregated capacity
	// but does not write effectiveQuotas status.
	//
	// +optional
	CapacityDistribution *CapacityDistribution `json:"capacityDistribution,omitempty"`
}

type CapacityDiscovery struct {
	// providers lists CapacityProvider objects consumed by this DQO.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Providers []CapacityDiscoveryProviderContribution `json:"providers"`
}

type CapacityDiscoveryProviderContribution struct {
	// name identifies a CapacityProvider.
	Name CapacityProviderName `json:"name"`

	// effectiveCapacityMultiplier specifies the multiplier applied to the
	// discovered capacity from this provider. It defaults to 1.
	//
	// +optional
	// +kubebuilder:default="1"
	EffectiveCapacityMultiplier *resource.Quantity `json:"effectiveCapacityMultiplier,omitempty"`
}

type CapacityDistribution struct {
	// subtreeRootQuotaRef identifies the root of the quota subtree.
	//
	// +required
	SubtreeRootQuotaRef CapacityDistributionSubtreeRootRef `json:"subtreeRootQuotaRef"`
}

type SubtreeRootRefKind string

const (
	ClusterQueueSubtreeRootRefKind SubtreeRootRefKind = "ClusterQueue"
	CohortSubtreeRootRefKind       SubtreeRootRefKind = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
	// kind indicates whether the subtree root is a ClusterQueue or Cohort.
	//
	// +required
	// +kubebuilder:validation:Enum=ClusterQueue;Cohort
	Kind SubtreeRootRefKind `json:"kind"`

	// name is the name of the ClusterQueue or Cohort that is the root of the quota subtree.
	//
	// +required
	Name string `json:"name"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type CapacityProviderName string

type DynamicQuotaOrchestratorStatus struct {
	// effectiveCapacity is the capacity aggregated from the referenced providers.
	//
	// +optional
	EffectiveCapacity *EffectiveCapacity `json:"effectiveCapacity,omitempty"`

	// conditions represents the current state of the DQO.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=16
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type EffectiveCapacity struct {
	// flavors lists aggregated capacity per flavor.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Flavors []EffectiveCapacityFlavor `json:"flavors"`
}

type EffectiveCapacityFlavor struct {
	// name is the name of the ResourceFlavor.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources lists aggregated capacities per resource within the flavor.
	//
	// +required
	Resources corev1.ResourceList `json:"resources"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Active",JSONPath=".status.conditions[?(@.type == 'Active')].status",type=string,description="Active condition of the DynamicQuotaOrchestrator"

// DynamicQuotaOrchestrator is the Schema for the dynamicquotaorchestrators API
type DynamicQuotaOrchestrator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DynamicQuotaOrchestratorSpec   `json:"spec,omitempty"`
	Status DynamicQuotaOrchestratorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DynamicQuotaOrchestratorList contains a list of DynamicQuotaOrchestrator
type DynamicQuotaOrchestratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DynamicQuotaOrchestrator `json:"items"`
}
