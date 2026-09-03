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

package capacityprovider

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

const (
	CapacityConfigMapKey                                                         = "capacity"
	TestCapacityProviderControllerName kueuealpha.CapacityProviderControllerName = "kueue.x-k8s.io/test-capacity-provider"
)

// TestCapacityConfig defines the schema expected in the test provider's ConfigMap data["capacity"].
type TestCapacityConfig struct {
	Flavors []TestCapacityFlavor `json:"flavors"`
}

// TestCapacityFlavor defines a flavor and its resources in the test capacity ConfigMap.
type TestCapacityFlavor struct {
	Name      kueuealpha.ResourceFlavorReference `json:"name"`
	Resources corev1.ResourceList                `json:"resources"`
}

func validateCapacityConfig(cfg *TestCapacityConfig) error {
	if len(cfg.Flavors) == 0 {
		return errors.New("must specify at least one flavor")
	}
	if len(cfg.Flavors) > 64 {
		return fmt.Errorf("flavors count %d exceeds maximum of 64", len(cfg.Flavors))
	}

	flavorNames := sets.New[kueuealpha.ResourceFlavorReference]()
	for _, f := range cfg.Flavors {
		if f.Name == "" {
			return errors.New("flavor name cannot be empty")
		}
		if flavorNames.Has(f.Name) {
			return fmt.Errorf("duplicate flavor name %q", f.Name)
		}
		flavorNames.Insert(f.Name)

		if len(f.Resources) == 0 {
			return fmt.Errorf("flavor %q must have between 1 and 64 resource entries", f.Name)
		}
		if len(f.Resources) > 64 {
			return fmt.Errorf("flavor %q has %d resources, exceeding maximum of 64", f.Name, len(f.Resources))
		}

		for rName, qty := range f.Resources {
			if rName == "" {
				return fmt.Errorf("resource name cannot be empty in flavor %q", f.Name)
			}
			if qty.Sign() < 0 {
				return fmt.Errorf("negative quantity %v for resource %q in flavor %q", qty.String(), rName, f.Name)
			}
		}
	}
	return nil
}
