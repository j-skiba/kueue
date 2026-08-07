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

package resourcegroups

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestEffectiveResourceGroups(t *testing.T) {
	specRGs := []kueue.ResourceGroup{
		{
			CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
			Flavors: []kueue.FlavorQuotas{
				{
					Name: "spec-flavor",
					Resources: []kueue.ResourceQuota{
						{Name: corev1.ResourceCPU, NominalQuota: resource.MustParse("10")},
					},
				},
			},
		},
	}
	statusRGs := []kueue.ResourceGroup{
		{
			CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
			Flavors: []kueue.FlavorQuotas{
				{
					Name: "effective-flavor",
					Resources: []kueue.ResourceQuota{
						{Name: corev1.ResourceCPU, NominalQuota: resource.MustParse("20")},
					},
				},
			},
		},
	}

	cq := &kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cq"},
		Spec: kueue.ClusterQueueSpec{
			ResourceGroups: specRGs,
		},
		Status: kueue.ClusterQueueStatus{
			EffectiveQuota: &kueue.EffectiveQuotaStatus{
				ResourceGroups: statusRGs,
			},
		},
	}

	cohort := &kueue.Cohort{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cohort"},
		Spec: kueue.CohortSpec{
			ResourceGroups: specRGs,
		},
		Status: kueue.CohortStatus{
			EffectiveQuota: &kueue.EffectiveQuotaStatus{
				ResourceGroups: statusRGs,
			},
		},
	}

	cqNoEffective := &kueue.ClusterQueue{Spec: kueue.ClusterQueueSpec{ResourceGroups: specRGs}}
	cohortNoEffective := &kueue.Cohort{Spec: kueue.CohortSpec{ResourceGroups: specRGs}}

	cases := map[string]struct {
		dynamicQuota bool
		cq           *kueue.ClusterQueue
		cohort       *kueue.Cohort
		wantRGs      []kueue.ResourceGroup
	}{
		"DynamicQuota disabled returns spec": {
			dynamicQuota: false,
			cq:           cq,
			cohort:       cohort,
			wantRGs:      specRGs,
		},
		"DynamicQuota enabled returns status effectiveQuota when set": {
			dynamicQuota: true,
			cq:           cq,
			cohort:       cohort,
			wantRGs:      statusRGs,
		},
		"DynamicQuota enabled returns spec when effectiveQuota is nil": {
			dynamicQuota: true,
			cq:           cqNoEffective,
			cohort:       cohortNoEffective,
			wantRGs:      specRGs,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuota, tc.dynamicQuota)

			if diff := cmp.Diff(tc.wantRGs, EffectiveResourceGroups(tc.cq)); diff != "" {
				t.Errorf("Unexpected EffectiveResourceGroups (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRGs, EffectiveCohortResourceGroups(tc.cohort)); diff != "" {
				t.Errorf("Unexpected EffectiveCohortResourceGroups (-want +got):\n%s", diff)
			}
		})
	}
}
