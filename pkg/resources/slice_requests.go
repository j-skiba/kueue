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
	"hash/fnv"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// HashResourceName computes a 64-bit FNV-1a hash of a ResourceName.
func HashResourceName(name corev1.ResourceName) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

// ResourceEntry encapsulates a single resource name, its pre-computed 64-bit hash, and its value.
type ResourceEntry struct {
	Name  corev1.ResourceName
	Hash  uint64
	Value int64
}

// SliceRequests represents resource requests as a single sorted slice of ResourceEntry structs.
// Sorted by uint64 Hash to enable fast O(M+N) two-pointer merge operations.
type SliceRequests []ResourceEntry

// NewSliceRequests constructs a SliceRequests from a Requests map.
func NewSliceRequests(r Requests) SliceRequests {
	if len(r) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(r))
	for name, val := range r {
		if val != 0 {
			sr = append(sr, ResourceEntry{
				Name:  name,
				Hash:  HashResourceName(name),
				Value: val,
			})
		}
	}
	sr.sort()
	return sr
}

// ResourceListToSliceRequests constructs a SliceRequests from a corev1.ResourceList.
func ResourceListToSliceRequests(rl corev1.ResourceList) SliceRequests {
	if len(rl) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(rl))
	for name, q := range rl {
		if val := ResourceValue(name, q); val != 0 {
			sr = append(sr, ResourceEntry{
				Name:  name,
				Hash:  HashResourceName(name),
				Value: val,
			})
		}
	}
	sr.sort()
	return sr
}

func (sr SliceRequests) sort() {
	slices.SortFunc(sr, func(a, b ResourceEntry) int {
		if a.Hash < b.Hash {
			return -1
		}
		if a.Hash > b.Hash {
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
}

// ToRequests converts SliceRequests back to a Requests map.
func (sr SliceRequests) ToRequests() Requests {
	r := make(Requests, len(sr))
	for _, entry := range sr {
		if entry.Value != 0 {
			r[entry.Name] = entry.Value
		}
	}
	return r
}

// Clone returns a deep copy of SliceRequests.
func (sr SliceRequests) Clone() SliceRequests {
	return slices.Clone(sr)
}

// Add performs an element-wise addition. Operates in a single pass with zero allocations when resource keys match.
func (sr *SliceRequests) Add(other SliceRequests) {
	if len(*sr) == len(other) {
		i := 0
		for i < len(*sr) && (*sr)[i].Hash == other[i].Hash {
			(*sr)[i].Value += other[i].Value
			i++
		}
		if i == len(*sr) {
			return
		}
		for k := 0; k < i; k++ {
			(*sr)[k].Value -= other[k].Value
		}
	}
	*sr = sr.MergeWith(other, func(a, b int64) int64 {
		return a + b
	})
}

// Sub performs an element-wise subtraction. Operates in a single pass with zero allocations when resource keys match.
func (sr *SliceRequests) Sub(other SliceRequests) {
	if len(*sr) == len(other) {
		i := 0
		for i < len(*sr) && (*sr)[i].Hash == other[i].Hash {
			(*sr)[i].Value -= other[i].Value
			i++
		}
		if i == len(*sr) {
			return
		}
		for k := 0; k < i; k++ {
			(*sr)[k].Value += other[k].Value
		}
	}
	*sr = sr.MergeWith(other, func(a, b int64) int64 {
		return a - b
	})
}

// MergeFunc defines a computation lambda between matching or missing values in two SliceRequests.
type MergeFunc func(valA, valB int64) int64

// MergeWith creates a new SliceRequests by walking both instances in two-pointer order
// and applying fn to calculate the resulting value for each resource.
func (sr SliceRequests) MergeWith(other SliceRequests, fn MergeFunc) SliceRequests {
	if len(sr) == 0 && len(other) == 0 {
		return nil
	}
	result := make(SliceRequests, 0, max(len(sr), len(other)))
	i, j := 0, 0

	for i < len(sr) && j < len(other) {
		h1, h2 := sr[i].Hash, other[j].Hash
		if h1 == h2 {
			appendEntry(&result, sr[i], fn(sr[i].Value, other[j].Value))
			i++
			j++
		} else if h1 < h2 {
			appendEntry(&result, sr[i], fn(sr[i].Value, 0))
			i++
		} else {
			appendEntry(&result, other[j], fn(0, other[j].Value))
			j++
		}
	}

	for ; i < len(sr); i++ {
		appendEntry(&result, sr[i], fn(sr[i].Value, 0))
	}
	for ; j < len(other); j++ {
		appendEntry(&result, other[j], fn(0, other[j].Value))
	}

	return result
}

func appendEntry(dst *SliceRequests, entry ResourceEntry, val int64) {
	if val != 0 {
		entry.Value = val
		*dst = append(*dst, entry)
	}
}
