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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

type DynamicQuotaOrchestratorWrapper struct {
	kueuealpha.DynamicQuotaOrchestrator
}

func MakeDynamicQuotaOrchestrator(name string) *DynamicQuotaOrchestratorWrapper {
	return &DynamicQuotaOrchestratorWrapper{
		kueuealpha.DynamicQuotaOrchestrator{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (w *DynamicQuotaOrchestratorWrapper) Obj() *kueuealpha.DynamicQuotaOrchestrator {
	return &w.DynamicQuotaOrchestrator
}

func (w *DynamicQuotaOrchestratorWrapper) DiscoveryProvider(name kueuealpha.CapacityProviderName, multiplier *resource.Quantity) *DynamicQuotaOrchestratorWrapper {
	w.Spec.CapacityDiscovery.Providers = append(w.Spec.CapacityDiscovery.Providers, kueuealpha.CapacityDiscoveryProviderContribution{
		Name:                        name,
		EffectiveCapacityMultiplier: multiplier,
	})
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) SubtreeRoot(kind kueuealpha.SubtreeRootRefKind, name string) *DynamicQuotaOrchestratorWrapper {
	w.Spec.CapacityDistribution = &kueuealpha.CapacityDistribution{
		SubtreeRootQuotaRef: kueuealpha.CapacityDistributionSubtreeRootRef{
			Kind: kind,
			Name: name,
		},
	}
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) EffectiveCapacity(flavors ...kueuealpha.EffectiveCapacityFlavor) *DynamicQuotaOrchestratorWrapper {
	w.Status.EffectiveCapacity = &kueuealpha.EffectiveCapacity{
		Flavors: flavors,
	}
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) EffectiveFlavor(name kueuealpha.ResourceFlavorReference, resources corev1.ResourceList) *DynamicQuotaOrchestratorWrapper {
	if w.Status.EffectiveCapacity == nil {
		w.Status.EffectiveCapacity = &kueuealpha.EffectiveCapacity{}
	}
	w.Status.EffectiveCapacity.Flavors = append(w.Status.EffectiveCapacity.Flavors, kueuealpha.EffectiveCapacityFlavor{
		Name:      name,
		Resources: resources,
	})
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) CreationTimestamp(t time.Time) *DynamicQuotaOrchestratorWrapper {
	w.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) UID(uid types.UID) *DynamicQuotaOrchestratorWrapper {
	w.ObjectMeta.UID = uid
	return w
}

func (w *DynamicQuotaOrchestratorWrapper) Condition(condition metav1.Condition) *DynamicQuotaOrchestratorWrapper {
	w.Status.Conditions = append(w.Status.Conditions, condition)
	return w
}

type CapacityProviderWrapper struct {
	kueuealpha.CapacityProvider
}

func MakeCapacityProvider(name string) *CapacityProviderWrapper {
	return &CapacityProviderWrapper{
		kueuealpha.CapacityProvider{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (w *CapacityProviderWrapper) Obj() *kueuealpha.CapacityProvider {
	return &w.CapacityProvider
}

func (w *CapacityProviderWrapper) ControllerName(controllerName kueuealpha.CapacityProviderControllerName) *CapacityProviderWrapper {
	w.Spec.ControllerName = controllerName
	return w
}

func (w *CapacityProviderWrapper) OrchestratedFlavors(flavors ...kueuealpha.ResourceFlavorReference) *CapacityProviderWrapper {
	for _, f := range flavors {
		w.Spec.OrchestratedFlavors = append(w.Spec.OrchestratedFlavors, kueuealpha.CapacityProviderOrchestratedFlavor{
			Name: f,
		})
	}
	return w
}

func (w *CapacityProviderWrapper) Parameters(apiGroup, kind, name string) *CapacityProviderWrapper {
	w.Spec.Parameters = &kueuealpha.CapacityProviderParametersReference{
		APIGroup: apiGroup,
		Kind:     kind,
		Name:     name,
	}
	return w
}

func (w *CapacityProviderWrapper) Capacity(capacity *kueuealpha.CapacityProviderNormalizedCapacity) *CapacityProviderWrapper {
	w.Status.Capacity = capacity
	return w
}

func (w *CapacityProviderWrapper) Condition(condition metav1.Condition) *CapacityProviderWrapper {
	w.Status.Conditions = append(w.Status.Conditions, condition)
	return w
}

type CapacityProviderNormalizedCapacityWrapper struct {
	kueuealpha.CapacityProviderNormalizedCapacity
}

func MakeNormalizedCapacity() *CapacityProviderNormalizedCapacityWrapper {
	return &CapacityProviderNormalizedCapacityWrapper{}
}

func (w *CapacityProviderNormalizedCapacityWrapper) Flavor(name kueuealpha.ResourceFlavorReference, resources corev1.ResourceList) *CapacityProviderNormalizedCapacityWrapper {
	w.Flavors = append(w.Flavors, kueuealpha.CapacityProviderNormalizedCapacityFlavor{
		Name:      name,
		Resources: resources,
	})
	return w
}

func (w *CapacityProviderNormalizedCapacityWrapper) Obj() *kueuealpha.CapacityProviderNormalizedCapacity {
	return &w.CapacityProviderNormalizedCapacity
}

type EffectiveCapacityWrapper struct {
	kueuealpha.EffectiveCapacity
}

func MakeEffectiveCapacity() *EffectiveCapacityWrapper {
	return &EffectiveCapacityWrapper{}
}

func (w *EffectiveCapacityWrapper) Flavor(name kueuealpha.ResourceFlavorReference, resources corev1.ResourceList) *EffectiveCapacityWrapper {
	w.Flavors = append(w.Flavors, kueuealpha.EffectiveCapacityFlavor{
		Name:      name,
		Resources: resources,
	})
	return w
}

func (w *EffectiveCapacityWrapper) Obj() *kueuealpha.EffectiveCapacity {
	return &w.EffectiveCapacity
}
