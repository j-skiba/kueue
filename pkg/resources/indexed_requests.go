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

package resources

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// NumRequests maps ResourceName to value using indexes from ResourceMapper.
type NumRequests []int64

// ResourceMapper maps ResourceName to index in NumRequests.
type ResourceMapper struct {
	Resources []corev1.ResourceName
	Index     map[corev1.ResourceName]int
}

// NewResourceMapper creates a new ResourceMapper.
func NewResourceMapper(resourceNames []corev1.ResourceName) *ResourceMapper {
	sortedNames := slices.Clone(resourceNames)
	slices.Sort(sortedNames)
	index := make(map[corev1.ResourceName]int, len(sortedNames))
	for i, name := range sortedNames {
		index[name] = i
	}
	return &ResourceMapper{
		Resources: sortedNames,
		Index:     index,
	}
}

// ToNumRequests converts Requests to NumRequests.
func (m *ResourceMapper) ToNumRequests(r Requests) NumRequests {
	nr := make(NumRequests, len(m.Resources))
	for resName, val := range r {
		if idx, ok := m.Index[resName]; ok {
			nr[idx] = val
		}
	}
	return nr
}

// ResourceListToNumRequests converts ResourceList to NumRequests.
func (m *ResourceMapper) ResourceListToNumRequests(rl corev1.ResourceList) NumRequests {
	nr := make(NumRequests, len(m.Resources))
	for resName, q := range rl {
		if idx, ok := m.Index[resName]; ok {
			nr[idx] = ResourceValue(resName, q)
		}
	}
	return nr
}

// ToRequests converts NumRequests to Requests.
func (m *ResourceMapper) ToRequests(nr NumRequests) Requests {
	r := make(Requests, len(m.Resources))
	limit := min(len(nr), len(m.Resources))
	for i := 0; i < limit; i++ {
		if val := nr[i]; val != 0 {
			r[m.Resources[i]] = val
		}
	}
	return r
}

// HasAllResources checks if Requests contains only resources known to the mapper.
func (m *ResourceMapper) HasAllResources(r Requests) bool {
	for resName, val := range r {
		if val == 0 {
			continue
		}
		if _, found := m.Index[resName]; !found {
			return false
		}
	}
	return true
}

// Clone returns a copy of NumRequests.
func (nr NumRequests) Clone() NumRequests {
	cloned := make(NumRequests, len(nr))
	copy(cloned, nr)
	return cloned
}

// Add adds other NumRequests to the receiver.
func (nr NumRequests) Add(other NumRequests) {
	limit := min(len(nr), len(other))
	for i := 0; i < limit; i++ {
		nr[i] += other[i]
	}
}

// Sub subtracts other NumRequests from the receiver.
func (nr NumRequests) Sub(other NumRequests) {
	limit := min(len(nr), len(other))
	for i := 0; i < limit; i++ {
		nr[i] -= other[i]
	}
}
