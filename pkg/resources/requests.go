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
	"maps"
	"math"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

var binaryFormattedResources sync.Map

// RegisterBinaryFormattedResource marks a resource name as byte-valued for display.
// Counter-based DRA logical resources (for example gpu.memory) should be registered
// at startup so quantities serialize with BinarySI units.
func RegisterBinaryFormattedResource(name corev1.ResourceName) {
	binaryFormattedResources.Store(name, struct{}{})
}

func usesBinaryFormat(name corev1.ResourceName) bool {
	_, ok := binaryFormattedResources.Load(name)
	return ok
}

// Requests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
type Requests map[corev1.ResourceName]int64

func NewRequests(rl corev1.ResourceList) Requests {
	r := Requests{}
	for name, quant := range rl {
		r[name] = ResourceValue(name, quant)
	}
	return r
}

func NewRequestsFromPodSpec(podSpec *corev1.PodSpec) Requests {
	return NewRequests(resourcehelpers.PodRequests(&corev1.Pod{Spec: *podSpec}, resourcehelpers.PodResourcesOptions{}))
}

func (r Requests) Clone() Requests {
	return maps.Clone(r)
}

func (r Requests) CloneResourceRequests() ResourceRequests {
	return r.Clone()
}

func (r Requests) ScaledUp(f int64) Requests {
	ret := r.Clone()
	ret.Mul(f)
	return ret
}

func (r Requests) ScaledDown(f int64) Requests {
	ret := r.Clone()
	ret.Divide(f)
	return ret
}

func (r Requests) Divide(f int64) {
	for k := range r {
		if r[k] == 0 && f == 0 {
			continue
		}
		r[k] /= f
	}
}

func (r Requests) Mul(f int64) {
	for k := range r {
		r[k] = utilmath.SaturatingMul(r[k], f)
	}
}

func (r Requests) Add(other ResourceRequests) {
	if r == nil || other == nil {
		return
	}
	for k, v := range other.ToRequests() {
		r[k] += v
	}
}

func (r Requests) Sub(other ResourceRequests) {
	if r == nil || other == nil {
		return
	}
	for k, v := range other.ToRequests() {
		r[k] -= v
	}
}

func (r Requests) CountIn(capacity ResourceRequests) int32 {
	if capacity == nil {
		return 0
	}
	count, _ := r.CountInWithLimitingResource(capacity)
	return count
}

// CountInWithLimitingResource returns how many times the request fits into capacity
// and the resource that is most constraining (i.e., gave the minimum count).
// When multiple resources have the same count, ties are broken alphabetically
// by resource name for determinism.
func (r Requests) CountInWithLimitingResource(capacity ResourceRequests) (int32, corev1.ResourceName) {
	if capacity == nil {
		return 0, ""
	}
	capMap := capacity.ToRequests()
	var (
		result           *int32
		limitingResource corev1.ResourceName
	)
	for rName, rValue := range r {
		capVal, found := capMap[rName]
		if !found && rValue != 0 {
			return 0, rName
		}
		// find the minimum count matching all the resource quota.
		var count int32
		if rValue == 0 {
			count = int32(math.MaxInt32)
		} else {
			// Clamp to 0: when an extended-resource allocatable on a node
			// drops below current usage mid-workload (e.g. GPU lost to a
			// driver issue, SKU removed, or NFD label flap), the TAS
			// snapshot's per-domain cap (allocatable - inUse) can go
			// negative. Integer division would then yield a negative count
			// and propagate into TopologyDomain.Count, which the apiserver
			// rejects with "podCounts.individual[X] in body should be greater
			// than or equal to 1", permanently wedging the workload. A
			// negative "fits N times" is meaningless; treat it as 0 so the
			// scheduler skips the over-subscribed domain instead.
			count = max(int32(capVal/rValue), 0)
		}
		// Tie-break between CPU and memory counts to ensure deterministic results.
		if result == nil || count < *result || (count == *result && rName < limitingResource) {
			result = &count
			limitingResource = rName
		}
	}
	return ptr.Deref(result, 0), limitingResource
}

func (r Requests) ToRequests() Requests {
	return r
}

func (r Requests) SerializeDetails() map[corev1.ResourceName]string {
	details := make(map[corev1.ResourceName]string, len(r))
	for resName, val := range r {
		details[resName] = ResourceQuantityString(resName, val)
	}
	return details
}

func (r Requests) ToResourceList() corev1.ResourceList {
	ret := make(corev1.ResourceList, len(r))
	for k, v := range r {
		ret[k] = ResourceQuantity(k, v)
	}
	return ret
}

// ResourceValue returns the integer value for the resource name.
// It's milli-units for CPU and absolute units for everything else.
func ResourceValue(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return utilmath.SafeMilliValue(q)
	}
	return q.Value()
}

func ResourceQuantity(name corev1.ResourceName, v int64) resource.Quantity {
	switch name {
	case corev1.ResourceCPU:
		return *resource.NewMilliQuantity(v, resource.DecimalSI)
	case corev1.ResourceMemory, corev1.ResourceEphemeralStorage:
		return newCanonicalQuantity(v, resource.BinarySI)
	default:
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) || usesBinaryFormat(name) {
			return newCanonicalQuantity(v, resource.BinarySI)
		}
		return *resource.NewQuantity(v, resource.DecimalSI)
	}
}

func newCanonicalQuantity(v int64, preferredFormat resource.Format) resource.Quantity {
	preferred := *resource.NewQuantity(v, preferredFormat)
	final, err := resource.ParseQuantity(preferred.String())
	if err != nil {
		return preferred
	}
	return final
}

func ResourceQuantityString(name corev1.ResourceName, v int64) string {
	rq := ResourceQuantity(name, v)
	return rq.String()
}

func AmountQuantityString(name corev1.ResourceName, a Amount) string {
	if a.Equal(Unlimited) {
		return Unlimited.String()
	}
	return ResourceQuantityString(name, a.Int64())
}

func (r Requests) GreaterKeys(other Requests) []corev1.ResourceName {
	if len(r) == 0 || len(other) == 0 {
		return nil
	}
	var result []corev1.ResourceName
	for name, value := range r {
		if otherValue, found := other[name]; found && value > otherValue {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (r Requests) GreaterKeysRL(rl corev1.ResourceList) []corev1.ResourceName {
	return r.GreaterKeys(NewRequests(rl))
}

func (r Requests) IsEmpty() bool {
	return len(r) == 0
}

func (r Requests) CreateEmpty() ResourceRequests {
	return Requests{}
}
