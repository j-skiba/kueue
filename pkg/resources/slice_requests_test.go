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
	"k8s.io/apimachinery/pkg/api/resource"
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

	sr1.Add(&sr2)
	wantAdd := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 3072,
		"nvidia.com/gpu":      1,
	}
	if diff := cmp.Diff(wantAdd, sr1.ToRequests()); diff != "" {
		t.Errorf("Add mismatch (-want +got):\n%s", diff)
	}

	sr1.Sub(&sr2)
	wantSub := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
	}
	if diff := cmp.Diff(wantSub, sr1.ToRequests()); diff != "" {
		t.Errorf("Sub mismatch (-want +got):\n%s", diff)
	}
}

func TestSliceRequestsCountIn(t *testing.T) {
	capacity := NewSliceRequests(Requests{
		corev1.ResourceCPU:    10000,
		corev1.ResourceMemory: 20480,
		corev1.ResourcePods:   100,
	})

	req := NewSliceRequests(Requests{
		corev1.ResourceCPU:    2000,
		corev1.ResourceMemory: 2048,
		corev1.ResourcePods:   10,
	})

	count := req.CountIn(&capacity)
	if count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}

	countLimiting, limitingRes := req.CountInWithLimitingResource(&capacity)
	if countLimiting != 5 {
		t.Errorf("expected limiting count 5, got %d", countLimiting)
	}
	if limitingRes != corev1.ResourceCPU {
		t.Errorf("expected limiting resource cpu, got %s", limitingRes)
	}
}

func TestSliceRequestsNonExistentResource(t *testing.T) {
	capacity := NewSliceRequests(Requests{
		corev1.ResourceCPU:    10000,
		corev1.ResourceMemory: 20480,
	})

	req := NewSliceRequests(Requests{
		corev1.ResourceCPU:      1000,
		"example.com/bogus-gpu": 1,
	})

	count := req.CountIn(&capacity)
	if count != 0 {
		t.Errorf("expected CountIn 0 for non-existent resource, got %d", count)
	}

	countLimiting, limitingRes := req.CountInWithLimitingResource(&capacity)
	if countLimiting != 0 {
		t.Errorf("expected count 0 for non-existent resource, got %d", countLimiting)
	}
	if limitingRes != "example.com/bogus-gpu" {
		t.Errorf("expected limiting resource 'example.com/bogus-gpu', got %s", limitingRes)
	}
}

func TestResourceListToSliceRequests(t *testing.T) {
	rl := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}

	sr := ResourceListToSliceRequests(rl)
	wantMap := Requests{
		corev1.ResourceCPU:    2000,
		corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
	}

	if diff := cmp.Diff(wantMap, sr.ToRequests()); diff != "" {
		t.Errorf("ResourceListToSliceRequests mismatch (-want +got):\n%s", diff)
	}
}

func TestLazySliceRequests(t *testing.T) {
	base := NewSliceRequests(Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
	})

	lazy := NewLazyRequests(&base)
	if !lazy.IsValid() {
		t.Errorf("expected Lazy to be valid")
	}

	sub := NewSliceRequests(Requests{
		corev1.ResourceMemory: 1024,
	})

	lazy.Sub(&sub)

	want := Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 1024,
	}

	if diff := cmp.Diff(want, lazy.Get().ToRequests()); diff != "" {
		t.Errorf("Lazy[SliceRequests] Sub mismatch (-want +got):\n%s", diff)
	}
}
