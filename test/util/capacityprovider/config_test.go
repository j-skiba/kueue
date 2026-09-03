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
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

func TestValidateCapacityConfig(t *testing.T) {
	cases := map[string]struct {
		cfg     TestCapacityConfig
		wantErr string
	}{
		"valid single flavor and resource": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "f1", Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
				},
			},
		},
		"empty flavors": {
			cfg:     TestCapacityConfig{},
			wantErr: "must specify at least one flavor",
		},
		"exceeds 64 flavors": {
			cfg: func() TestCapacityConfig {
				flavors := make([]TestCapacityFlavor, 65)
				for i := range flavors {
					flavors[i] = TestCapacityFlavor{
						Name:      kueuealpha.ResourceFlavorReference(fmt.Sprintf("f%d", i)),
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}
				}
				return TestCapacityConfig{Flavors: flavors}
			}(),
			wantErr: "flavors count 65 exceeds maximum of 64",
		},
		"empty flavor name": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "", Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
				},
			},
			wantErr: "flavor name cannot be empty",
		},
		"duplicate flavor name": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "f1", Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
					{Name: "f1", Resources: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}},
				},
			},
			wantErr: `duplicate flavor name "f1"`,
		},
		"flavor has no resources": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "f1", Resources: corev1.ResourceList{}},
				},
			},
			wantErr: `flavor "f1" must have between 1 and 64 resource entries`,
		},
		"flavor exceeds 64 resources": {
			cfg: func() TestCapacityConfig {
				res := corev1.ResourceList{}
				for i := range 65 {
					res[corev1.ResourceName(fmt.Sprintf("res%d", i))] = resource.MustParse("1")
				}
				return TestCapacityConfig{
					Flavors: []TestCapacityFlavor{
						{Name: "f1", Resources: res},
					},
				}
			}(),
			wantErr: `flavor "f1" has 65 resources, exceeding maximum of 64`,
		},
		"empty resource name": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "f1", Resources: corev1.ResourceList{"": resource.MustParse("1")}},
				},
			},
			wantErr: `resource name cannot be empty in flavor "f1"`,
		},
		"negative quantity": {
			cfg: TestCapacityConfig{
				Flavors: []TestCapacityFlavor{
					{Name: "f1", Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-5")}},
				},
			},
			wantErr: `negative quantity -5 for resource "cpu" in flavor "f1"`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateCapacityConfig(&tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
				}
			}
		})
	}
}
