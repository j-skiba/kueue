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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// BenchmarkHighFrequencyAddSub measures intensive Add and Sub operations
// across node targets (50, 500, 1000, 5000) using modern b.Loop().
func BenchmarkHighFrequencyAddSub(b *testing.B) {
	nodeCounts := []int{50, 500, 1000, 5000}
	numRes := 30

	resList := make([]corev1.ResourceName, numRes)
	resList[0] = corev1.ResourceCPU
	resList[1] = corev1.ResourceMemory
	resList[2] = corev1.ResourcePods

	capMap := make(Requests, numRes)
	reqMap := make(Requests, numRes)

	capMap[corev1.ResourceCPU] = 10000
	capMap[corev1.ResourceMemory] = 204800
	capMap[corev1.ResourcePods] = 110

	reqMap[corev1.ResourceCPU] = 1000
	reqMap[corev1.ResourceMemory] = 2048
	reqMap[corev1.ResourcePods] = 1

	for i := 3; i < numRes; i++ {
		name := corev1.ResourceName(fmt.Sprintf("example.com/custom-resource-%d", i))
		resList[i] = name
		capMap[name] = 100
		reqMap[name] = 10
	}

	mapper := NewResourceMapper(resList)

	for _, numNodes := range nodeCounts {
		b.Run(fmt.Sprintf("AddSub/Legacy_Map/nodes=%d", numNodes), func(b *testing.B) {
			caps := make([]Requests, numNodes)
			for i := 0; i < numNodes; i++ {
				caps[i] = capMap.Clone()
			}
			b.ReportAllocs()

			for b.Loop() {
				for j := 0; j < numNodes; j++ {
					caps[j].Add(reqMap)
				}
				for j := 0; j < numNodes; j++ {
					caps[j].Sub(reqMap)
				}
			}
		})

		b.Run(fmt.Sprintf("AddSub/v1_NumRequests/nodes=%d", numNodes), func(b *testing.B) {
			capNum := mapper.ToNumRequests(capMap)
			reqNum := mapper.ToNumRequests(reqMap)

			caps := make([]NumRequests, numNodes)
			for i := 0; i < numNodes; i++ {
				caps[i] = capNum.Clone()
			}
			b.ReportAllocs()

			for b.Loop() {
				for j := 0; j < numNodes; j++ {
					caps[j].Add(reqNum)
				}
				for j := 0; j < numNodes; j++ {
					caps[j].Sub(reqNum)
				}
			}
		})

		b.Run(fmt.Sprintf("AddSub/v2_SliceRequests/nodes=%d", numNodes), func(b *testing.B) {
			capSlice := NewSliceRequests(capMap)
			reqSlice := NewSliceRequests(reqMap)

			caps := make([]SliceRequests, numNodes)
			for i := 0; i < numNodes; i++ {
				caps[i] = capSlice.Clone()
			}
			b.ReportAllocs()

			for b.Loop() {
				for j := 0; j < numNodes; j++ {
					caps[j].Add(reqSlice)
				}
				for j := 0; j < numNodes; j++ {
					caps[j].Sub(reqSlice)
				}
			}
		})
	}
}
