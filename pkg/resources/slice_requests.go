/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
Distributed under the License is distributed on an "AS IS" BASIS,
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
func (sr *SliceRequests) ToRequests() Requests {
	if sr == nil || len(*sr) == 0 {
		return nil
	}
	req := make(Requests, len(*sr))
	for _, entry := range *sr {
		if entry.Value != 0 {
			req[entry.Name] = entry.Value
		}
	}
	return req
}

func (sr SliceRequests) ToRequestsValue() Requests {
	return sr.ToRequests()
}

func (sr SliceRequests) Clone() SliceRequests {
	res := make(SliceRequests, len(sr))
	copy(res, sr)
	return res
}

func (sr *SliceRequests) CloneResourceRequests() ResourceRequests {
	if sr == nil {
		return (*SliceRequests)(nil)
	}
	res := make(SliceRequests, len(*sr))
	copy(res, *sr)
	return &res
}

// Add performs an element-wise addition.
func (sr *SliceRequests) Add(other ResourceRequests) {
	if other == nil || sr == nil {
		return
	}
	var otherSlice SliceRequests
	if s, ok := other.(*SliceRequests); ok && s != nil {
		otherSlice = *s
	} else {
		otherSlice = NewSliceRequests(other.ToRequests())
	}
	sr.AddSlice(otherSlice)
}

func (sr *SliceRequests) AddSlice(other SliceRequests) {
	if sr == nil {
		return
	}
	if *sr == nil {
		*sr = other.Clone()
		return
	}
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

// Sub performs an element-wise subtraction.
func (sr *SliceRequests) Sub(other ResourceRequests) {
	if other == nil || sr == nil {
		return
	}
	var otherSlice SliceRequests
	if s, ok := other.(*SliceRequests); ok && s != nil {
		otherSlice = *s
	} else {
		otherSlice = NewSliceRequests(other.ToRequests())
	}
	sr.SubSlice(otherSlice)
}

func (sr *SliceRequests) SubSlice(other SliceRequests) {
	if sr == nil || *sr == nil {
		return
	}
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

	for i < len(sr) {
		appendEntry(&result, sr[i], fn(sr[i].Value, 0))
		i++
	}

	for j < len(other) {
		appendEntry(&result, other[j], fn(0, other[j].Value))
		j++
	}

	return result
}

func appendEntry(result *SliceRequests, entry ResourceEntry, val int64) {
	if val != 0 {
		*result = append(*result, ResourceEntry{
			Name:  entry.Name,
			Hash:  entry.Hash,
			Value: val,
		})
	}
}

func (sr *SliceRequests) CountIn(capacity ResourceRequests) int32 {
	if capacity == nil || sr == nil {
		return 0
	}
	if capSlice, ok := capacity.(*SliceRequests); ok && capSlice != nil {
		return sr.CountInSlice(*capSlice)
	}
	return sr.CountInSlice(NewSliceRequests(capacity.ToRequests()))
}

func (sr SliceRequests) CountInSlice(capacity SliceRequests) int32 {
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

func (sr *SliceRequests) CountInWithLimitingResource(capacity ResourceRequests) (int32, corev1.ResourceName) {
	if capacity == nil || sr == nil {
		return 0, ""
	}
	if capSlice, ok := capacity.(*SliceRequests); ok && capSlice != nil {
		return sr.CountInWithLimitingResourceSlice(*capSlice)
	}
	return sr.CountInWithLimitingResourceSlice(NewSliceRequests(capacity.ToRequests()))
}

func (sr SliceRequests) CountInWithLimitingResourceSlice(capacity SliceRequests) (int32, corev1.ResourceName) {
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

func (sr *SliceRequests) SerializeDetails() map[corev1.ResourceName]string {
	if sr == nil {
		return nil
	}
	details := make(map[corev1.ResourceName]string, len(*sr))
	for _, entry := range *sr {
		details[entry.Name] = ResourceQuantityString(entry.Name, entry.Value)
	}
	return details
}

func (sr *SliceRequests) IsEmpty() bool {
	return sr == nil || len(*sr) == 0
}

func (sr *SliceRequests) CreateEmpty() ResourceRequests {
	empty := SliceRequests{}
	return &empty
}
