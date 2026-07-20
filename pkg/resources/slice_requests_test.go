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
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
)

func TestSliceRequestsConversion(t *testing.T) {
	reqMap := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
		"nvidia.com/gpu":      2,
	}

	sr := NewSliceRequests(reqMap)
	gotMap := sr.ToRequests()

	if diff := cmp.Diff(reqMap, gotMap); diff != "" {
		t.Errorf("ToRequests mismatch (-want +got):\n%s", diff)
	}
}

func TestSliceRequestsAddAndSub(t *testing.T) {
	sr1 := NewSliceRequests(Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
	})
	sr2 := NewSliceRequests(Requests{
		corev1.ResourceMemory: 1024,
		"nvidia.com/gpu":      1,
	})

	sr1.Add(sr2)
	wantAdd := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 3072,
		"nvidia.com/gpu":      1,
	}
	if diff := cmp.Diff(wantAdd, sr1.ToRequests()); diff != "" {
		t.Errorf("Add mismatch (-want +got):\n%s", diff)
	}

	sr1.Sub(sr2)
	wantSub := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
	}
	if diff := cmp.Diff(wantSub, sr1.ToRequests()); diff != "" {
		t.Errorf("Sub mismatch (-want +got):\n%s", diff)
	}
}
