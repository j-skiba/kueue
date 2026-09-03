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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// CapacityConfigMapWrapper is a fluent wrapper for constructing test ConfigMaps with capacity data.
type CapacityConfigMapWrapper struct {
	corev1.ConfigMap
	config TestCapacityConfig
}

func MakeCapacityConfigMap(name, ns string) *CapacityConfigMapWrapper {
	return &CapacityConfigMapWrapper{
		ConfigMap: corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Data: make(map[string]string),
		},
	}
}

// MakeCapacityConfigMapWithGenerateName creates a CapacityConfigMapWrapper using a generateName prefix.
func MakeCapacityConfigMapWithGenerateName(prefix, ns string) *CapacityConfigMapWrapper {
	return MakeCapacityConfigMap("", ns).GenerateName(prefix)
}

// GenerateName sets the GenerateName field on the ConfigMap.
func (w *CapacityConfigMapWrapper) GenerateName(prefix string) *CapacityConfigMapWrapper {
	w.ConfigMap.GenerateName = prefix
	return w
}

func (w *CapacityConfigMapWrapper) Flavor(name kueuealpha.ResourceFlavorReference, res corev1.ResourceList) *CapacityConfigMapWrapper {
	w.config.Flavors = append(w.config.Flavors, TestCapacityFlavor{
		Name:      name,
		Resources: res,
	})
	return w
}

func (w *CapacityConfigMapWrapper) RawData(key, value string) *CapacityConfigMapWrapper {
	w.Data[key] = value
	return w
}

func (w *CapacityConfigMapWrapper) Obj() *corev1.ConfigMap {
	if len(w.config.Flavors) > 0 {
		yamlBytes, err := yaml.Marshal(w.config)
		if err != nil {
			panic(err)
		}
		w.Data[CapacityConfigMapKey] = string(yamlBytes)
	}
	return &w.ConfigMap
}

// CapacityConfigWrapper is a fluent wrapper for constructing TestCapacityConfig.
type CapacityConfigWrapper struct {
	TestCapacityConfig
}

func MakeCapacityConfig() *CapacityConfigWrapper {
	return &CapacityConfigWrapper{}
}

func (w *CapacityConfigWrapper) Flavor(name kueuealpha.ResourceFlavorReference, res corev1.ResourceList) *CapacityConfigWrapper {
	w.Flavors = append(w.Flavors, TestCapacityFlavor{
		Name:      name,
		Resources: res,
	})
	return w
}

func (w *CapacityConfigWrapper) MustMarshal() string {
	return MustMarshalCapacityConfig(w.TestCapacityConfig)
}

func (w *CapacityConfigWrapper) Obj() TestCapacityConfig {
	return w.TestCapacityConfig
}

func MustMarshalCapacityConfig(cfg TestCapacityConfig) string {
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return string(yamlBytes)
}
