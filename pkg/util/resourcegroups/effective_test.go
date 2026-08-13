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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestEffectiveResourceGroups(t *testing.T) {
	specFlavor := utiltestingapi.MakeFlavorQuotas("spec-flavor").Resource(corev1.ResourceCPU, "10").FlavorQuotas
	effectiveFlavor := utiltestingapi.MakeFlavorQuotas("effective-flavor").Resource(corev1.ResourceCPU, "20").FlavorQuotas

	specRGs := []kueue.ResourceGroup{
		utiltestingapi.ResourceGroup(specFlavor),
	}
	statusRGs := []kueue.ResourceGroup{
		utiltestingapi.ResourceGroup(effectiveFlavor),
	}

	cq := utiltestingapi.MakeClusterQueue("test-cq").
		ResourceGroup(specFlavor).
		EffectiveResourceGroup(effectiveFlavor).
		Obj()

	cohort := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(specFlavor).
		EffectiveResourceGroup(effectiveFlavor).
		Obj()

	cqNoEffective := utiltestingapi.MakeClusterQueue("test-cq").
		ResourceGroup(specFlavor).
		Obj()
	cohortNoEffective := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(specFlavor).
		Obj()

	cqEmptyEffective := utiltestingapi.MakeClusterQueue("test-cq").
		ResourceGroup(specFlavor).
		Obj()
	cqEmptyEffective.Status.EffectiveQuota = &kueue.EffectiveQuotaStatus{ResourceGroups: []kueue.ResourceGroup{}}

	cohortEmptyEffective := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(specFlavor).
		Obj()
	cohortEmptyEffective.Status.EffectiveQuota = &kueue.EffectiveQuotaStatus{ResourceGroups: []kueue.ResourceGroup{}}

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
		"DynamicQuota enabled returns empty status effectiveQuota when set to empty": {
			dynamicQuota: true,
			cq:           cqEmptyEffective,
			cohort:       cohortEmptyEffective,
			wantRGs:      []kueue.ResourceGroup{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.dynamicQuota)

			if diff := cmp.Diff(tc.wantRGs, EffectiveResourceGroups(tc.cq)); diff != "" {
				t.Errorf("Unexpected EffectiveResourceGroups (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRGs, EffectiveCohortResourceGroups(tc.cohort)); diff != "" {
				t.Errorf("Unexpected EffectiveCohortResourceGroups (-want +got):\n%s", diff)
			}
		})
	}
}
