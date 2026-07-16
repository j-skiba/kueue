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
	"math"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// ResourceEntry represents a single resource quantity, sorted by FNV-1a hash.
type ResourceEntry struct {
	Name  corev1.ResourceName
	Hash  uint64
	Value int64
}

// SliceRequests represents a list of resource requests sorted numerically by hash.
type SliceRequests []ResourceEntry

// HashResource computes a 64-bit FNV-1a hash for a ResourceName.
func HashResource(name corev1.ResourceName) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return h.Sum64()
}

// NewSliceRequests creates a SliceRequests from a Requests map, sorted by hash.
func NewSliceRequests(req Requests) SliceRequests {
	if len(req) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(req))
	for name, val := range req {
		if val != 0 {
			sr = append(sr, ResourceEntry{
				Name:  name,
				Hash:  HashResource(name),
				Value: val,
			})
		}
	}
	slices.SortFunc(sr, func(a, b ResourceEntry) int {
		if a.Hash < b.Hash {
			return -1
		}
		if a.Hash > b.Hash {
			return 1
		}
		return 0
	})
	return sr
}

// ResourceListToSliceRequests creates a SliceRequests from a corev1.ResourceList.
func ResourceListToSliceRequests(rl corev1.ResourceList) SliceRequests {
	if len(rl) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(rl))
	for name, q := range rl {
		val := ResourceValue(name, q)
		if val != 0 {
			sr = append(sr, ResourceEntry{
				Name:  name,
				Hash:  HashResource(name),
				Value: val,
			})
		}
	}
	slices.SortFunc(sr, func(a, b ResourceEntry) int {
		if a.Hash < b.Hash {
			return -1
		}
		if a.Hash > b.Hash {
			return 1
		}
		return 0
	})
	return sr
}

// ToRequests converts a SliceRequests back to a Requests map.
func (sr SliceRequests) ToRequests() Requests {
	if len(sr) == 0 {
		return nil
	}
	req := make(Requests, len(sr))
	for _, entry := range sr {
		if entry.Value != 0 {
			req[entry.Name] = entry.Value
		}
	}
	return req
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

// VisitFunc defines a lambda function for two-pointer traversal over matching/missing resources.
// Returning false halts the traversal early.
type VisitFunc func(name corev1.ResourceName, valA, valB int64) bool

// ZipVisits walks two SliceRequests in parallel, invoking fn for each resource.
func (sr SliceRequests) ZipVisits(other SliceRequests, fn VisitFunc) {
	i, j := 0, 0
	for i < len(sr) || j < len(other) {
		var name corev1.ResourceName
		var valA, valB int64

		if i < len(sr) && (j >= len(other) || sr[i].Hash < other[j].Hash) {
			name, valA = sr[i].Name, sr[i].Value
			i++
		} else if j < len(other) && (i >= len(sr) || other[j].Hash < sr[i].Hash) {
			name, valB = other[j].Name, other[j].Value
			j++
		} else {
			name = sr[i].Name
			valA, valB = sr[i].Value, other[j].Value
			i++
			j++
		}

		if !fn(name, valA, valB) {
			break
		}
	}
}

// CountIn computes how many times sr fits into capacity using two-pointer merge.
// Exits early as soon as any requested resource cannot fit (count == 0).
func (sr SliceRequests) CountIn(capacity SliceRequests) int32 {
	if len(sr) == 0 {
		return math.MaxInt32
	}

	minCount := int32(math.MaxInt32)
	j := 0

	for _, req := range sr {
		for j < len(capacity) && capacity[j].Hash < req.Hash {
			j++
		}

		var capVal int64
		if j < len(capacity) && capacity[j].Hash == req.Hash {
			capVal = capacity[j].Value
		}

		if capVal <= 0 && req.Value != 0 {
			return 0
		}

		if req.Value > 0 {
			count := int32(capVal / req.Value)
			if count == 0 {
				return 0
			}
			if count < minCount {
				minCount = count
			}
		}
	}

	return minCount
}

// CountInWithLimitingResource returns how many times sr fits into capacity
// and the limiting ResourceName. Ties with equal counts are broken alphabetically.
func (sr SliceRequests) CountInWithLimitingResource(capacity SliceRequests) (int32, corev1.ResourceName) {
	if len(sr) == 0 {
		return math.MaxInt32, ""
	}

	minCount := int32(math.MaxInt32)
	var limitingRes corev1.ResourceName
	j := 0

	for _, req := range sr {
		for j < len(capacity) && capacity[j].Hash < req.Hash {
			j++
		}

		var capVal int64
		if j < len(capacity) && capacity[j].Hash == req.Hash {
			capVal = capacity[j].Value
		}

		if capVal < 0 && req.Value != 0 {
			return 0, req.Name
		}

		count := int32(math.MaxInt32)
		if req.Value > 0 {
			count = max(int32(capVal/req.Value), 0)
		}

		if limitingRes == "" || count < minCount || (count == minCount && req.Name < limitingRes) {
			minCount = count
			limitingRes = req.Name
		}
	}

	if limitingRes == "" {
		return 0, ""
	}
	return minCount, limitingRes
}
