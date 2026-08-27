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

package dynamicquotaorchestrator

import (
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
	capacityprovidertest "sigs.k8s.io/kueue/test/util/capacityprovider"
)

var _ = ginkgo.Describe("DynamicQuotaOrchestrator controller", ginkgo.Label("controller:dqo", "area:dynamicquotaorchestration"), func() {
	var (
		ns               *corev1.Namespace
		cp               *kueuealpha.CapacityProvider
		dqo              *kueuealpha.DynamicQuotaOrchestrator
		ancestorDQO      *kueuealpha.DynamicQuotaOrchestrator
		childDQO         *kueuealpha.DynamicQuotaOrchestrator
		cq               *kueue.ClusterQueue
		cq1              *kueue.ClusterQueue
		cq2              *kueue.ClusterQueue
		cq3              *kueue.ClusterQueue
		rootCohort       *kueue.Cohort
		childCohort      *kueue.Cohort
		grandchildCohort *kueue.Cohort
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)
		ns = utiltesting.MakeNamespaceWithGenerateName("dqo-")
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, childDQO, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, ancestorDQO, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq3, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, grandchildCohort, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, childCohort, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
		childDQO, ancestorDQO, dqo = nil, nil, nil
		cq, cq1, cq2, cq3 = nil, nil, nil, nil
		childCohort, grandchildCohort, rootCohort = nil, nil, nil
		cp = nil

		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should discover capacity from CapacityProvider and dynamically update", func() {
		cm := capacityprovidertest.MakeCapacityConfigMap("discovery-cm", ns.Name).
			Flavor("f1", corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100"),
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cm)

		cp = utiltestingalpha.MakeCapacityProvider("discovery-cp").
			ControllerName(kueuealpha.TestCapacityProviderControllerName).
			OrchestratedFlavors("f1").
			Parameters("k8s.io", "ConfigMap", "discovery-cm").
			Obj()
		util.MustCreate(ctx, k8sClient, cp)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("discovery-dqo").
			DiscoveryProvider("discovery-cp", nil).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying initial discovery aggregation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(latestDQO.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).Should(gomega.Equal(kueuealpha.DynamicQuotaOrchestratorReasonComputed))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("100"),
					}).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating capacity ConfigMap and verifying dynamic DQO update", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestCM corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: cm.Name}, &latestCM)).Should(gomega.Succeed())
				latestCM.Data[capacityprovidertest.CapacityConfigMapKey] = capacityprovidertest.MakeCapacityConfig().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("250"),
					}).
					MustMarshal()
				g.Expect(k8sClient.Update(ctx, &latestCM)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				wantUpdatedCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("250"),
					}).
					Obj()
				g.Expect(cmp.Diff(wantUpdatedCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should distribute capacity to a single ClusterQueue and update dynamically", func() {
		cm := capacityprovidertest.MakeCapacityConfigMap("dist-cq-cm", ns.Name).
			Flavor("f1", corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100"),
				corev1.ResourceMemory: resource.MustParse("50Gi"),
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cm)

		cp = utiltestingalpha.MakeCapacityProvider("dist-cq-cp").
			ControllerName(kueuealpha.TestCapacityProviderControllerName).
			OrchestratedFlavors("f1").
			Parameters("k8s.io", "ConfigMap", "dist-cq-cm").
			Obj()
		util.MustCreate(ctx, k8sClient, cp)

		cq = utiltestingapi.MakeClusterQueue("dist-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "20Gi").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("dist-dqo").
			DiscoveryProvider("dist-cq-cp", nil).
			SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "dist-cq").
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		cqKey := types.NamespacedName{Name: cq.Name}
		latestCQ := &kueue.ClusterQueue{}

		ginkgo.By("Verifying initial effective quota distribution", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqKey, latestCQ)).Should(gomega.Succeed())
				g.Expect(latestCQ.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(latestCQ.Status.EffectiveQuotas.OrchestratorRef).Should(gomega.Equal(kueue.EffectiveQuotaStatusOrchestratorRef{
					APIGroup: "kueue.x-k8s.io",
					Kind:     kueuealpha.DynamicQuotaOrchestratorKind,
					Name:     "dist-dqo",
				}))

				wantResourceGroups := []kueue.ResourceGroup{
					utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("f1").
							Resource(corev1.ResourceCPU, "100").
							Resource(corev1.ResourceMemory, "50Gi").
							Obj(),
					),
				}
				g.Expect(cmp.Diff(wantResourceGroups, latestCQ.Status.EffectiveQuotas.ResourceGroups, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating capacity and verifying ClusterQueue effective quota updates", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestCM corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: cm.Name}, &latestCM)).Should(gomega.Succeed())
				latestCM.Data[capacityprovidertest.CapacityConfigMapKey] = capacityprovidertest.MakeCapacityConfig().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200"),
						corev1.ResourceMemory: resource.MustParse("80Gi"),
					}).
					MustMarshal()
				g.Expect(k8sClient.Update(ctx, &latestCM)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqKey, latestCQ)).Should(gomega.Succeed())
				wantUpdatedResourceGroups := []kueue.ResourceGroup{
					utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("f1").
							Resource(corev1.ResourceCPU, "200").
							Resource(corev1.ResourceMemory, "80Gi").
							Obj(),
					),
				}
				g.Expect(cmp.Diff(wantUpdatedResourceGroups, latestCQ.Status.EffectiveQuotas.ResourceGroups, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	// Cohort tree hierarchy (3 levels):
	//
	//              root-cohort (Level 1)
	//             /           \
	//           cq-1       child-cohort (Level 2)
	//          (10 CPU)   /            \
	//                   cq-2     grandchild-cohort (Level 3)
	//                  (20 CPU)         |
	//                                  cq-3
	//                                 (30 CPU)
	ginkgo.It("Should distribute capacity proportionally across 3-level Cohort tree", func() {
		cm := capacityprovidertest.MakeCapacityConfigMap("cohort-tree-cm", ns.Name).
			Flavor("f1", corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("60"),
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cm)

		cp = utiltestingalpha.MakeCapacityProvider("cohort-tree-cp").
			ControllerName(kueuealpha.TestCapacityProviderControllerName).
			OrchestratedFlavors("f1").
			Parameters("k8s.io", "ConfigMap", "cohort-tree-cm").
			Obj()
		util.MustCreate(ctx, k8sClient, cp)

		rootCohort = utiltestingapi.MakeCohort("root-cohort").Obj()
		util.MustCreate(ctx, k8sClient, rootCohort)

		childCohort = utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj()
		util.MustCreate(ctx, k8sClient, childCohort)

		grandchildCohort = utiltestingapi.MakeCohort("grandchild-cohort").Parent("child-cohort").Obj()
		util.MustCreate(ctx, k8sClient, grandchildCohort)

		cq1 = utiltestingapi.MakeClusterQueue("cq-1").
			Cohort("root-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq1)

		cq2 = utiltestingapi.MakeClusterQueue("cq-2").
			Cohort("child-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "20").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq2)

		cq3 = utiltestingapi.MakeClusterQueue("cq-3").
			Cohort("grandchild-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "30").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq3)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("cohort-dqo").
			DiscoveryProvider("cohort-tree-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		cq1Key := types.NamespacedName{Name: cq1.Name}
		cq2Key := types.NamespacedName{Name: cq2.Name}
		cq3Key := types.NamespacedName{Name: cq3.Name}

		ginkgo.By("Verifying proportional distribution to CQs across all 3 levels of the cohort tree", func() {
			var gotCQ1, gotCQ2, gotCQ3 kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cq1Key, &gotCQ1)).Should(gomega.Succeed())
				g.Expect(gotCQ1.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ1.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("10")))

				g.Expect(k8sClient.Get(ctx, cq2Key, &gotCQ2)).Should(gomega.Succeed())
				g.Expect(gotCQ2.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ2.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("20")))

				g.Expect(k8sClient.Get(ctx, cq3Key, &gotCQ3)).Should(gomega.Succeed())
				g.Expect(gotCQ3.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ3.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("30")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should soft-validate overlapping DQOs and activate once conflict is removed", func() {
		cm := capacityprovidertest.MakeCapacityConfigMap("overlap-cm", ns.Name).
			Flavor("f1", corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100"),
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cm)

		cp = utiltestingalpha.MakeCapacityProvider("overlap-cp").
			ControllerName(kueuealpha.TestCapacityProviderControllerName).
			OrchestratedFlavors("f1").
			Parameters("k8s.io", "ConfigMap", "overlap-cm").
			Obj()
		util.MustCreate(ctx, k8sClient, cp)

		rootCohort = utiltestingapi.MakeCohort("overlap-root").Obj()
		util.MustCreate(ctx, k8sClient, rootCohort)

		childCohort = utiltestingapi.MakeCohort("overlap-child").Parent("overlap-root").Obj()
		util.MustCreate(ctx, k8sClient, childCohort)

		cq = utiltestingapi.MakeClusterQueue("overlap-cq").
			Cohort("overlap-child").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "50").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		ancestorDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("ancestor-dqo").
			DiscoveryProvider("overlap-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "overlap-root").
			Obj()
		util.MustCreate(ctx, k8sClient, ancestorDQO)

		childDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
			DiscoveryProvider("overlap-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "overlap-child").
			Obj()
		util.MustCreate(ctx, k8sClient, childDQO)

		childDQOKey := types.NamespacedName{Name: childDQO.Name}
		latestChildDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying child DQO is deactivated due to ancestor conflict", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childDQOKey, latestChildDQO)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(latestChildDQO.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).Should(gomega.Equal(kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting ancestor DQO and verifying child DQO becomes active", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ancestorDQO, true)
			ancestorDQO = nil

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childDQOKey, latestChildDQO)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(latestChildDQO.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).Should(gomega.Equal(kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
