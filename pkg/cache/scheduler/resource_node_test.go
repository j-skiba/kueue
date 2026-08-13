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

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestCohortLendable(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())

	cq1 := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").LendingLimit("8").Append().
				ResourceQuotaWrapper("example.com/gpu").NominalQuota("3").LendingLimit("3").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	cq2 := utiltestingapi.MakeClusterQueue("cq2").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").LendingLimit("2").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	if err := cache.AddClusterQueue(ctx, &cq1); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}
	if err := cache.AddClusterQueue(ctx, &cq2); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}

	wantLendable := map[corev1.ResourceName]resources.Amount{
		corev1.ResourceCPU: resources.NewAmount(10_000),
		"example.com/gpu":  resources.NewAmount(3),
	}

	lendable := calculateLendable(cache.hm.Cohort("test-cohort"))
	if diff := cmp.Diff(wantLendable, lendable); diff != "" {
		t.Errorf("Unexpected cohort lendable (-want,+got):\n%s", diff)
	}
}

func TestCohortEffectiveQuota(t *testing.T) {
	cohort := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).
		EffectiveResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "500").Obj(),
		).
		Obj()

	cases := map[string]struct {
		dynamicQuota bool
		wantNominal  int64
	}{
		"DynamicQuota disabled uses spec quotas for cohort": {
			dynamicQuota: false,
			wantNominal:  10000,
		},
		"DynamicQuota enabled uses effective quota for cohort": {
			dynamicQuota: true,
			wantNominal:  500000,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.dynamicQuota)

			c := newCohort("test-cohort")
			if err := c.updateCohort(cohort, nil); err != nil {
				t.Fatalf("unexpected error updating cohort: %v", err)
			}

			fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
			if got := c.resourceNode.Quotas[fr].Nominal.Int64(); got != tc.wantNominal {
				t.Errorf("Expected nominal quota %d, got %d", tc.wantNominal, got)
			}
		})
	}
}
