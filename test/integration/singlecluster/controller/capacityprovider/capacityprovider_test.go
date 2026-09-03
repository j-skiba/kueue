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
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util"
	capacityprovidertest "sigs.k8s.io/kueue/test/util/capacityprovider"
)

var _ = ginkgo.Describe("CapacityProvider test controller", ginkgo.Label("controller:capacityprovider", "area:dynamicquotaorchestration"), func() {
	var (
		ns *corev1.Namespace
		cp *kueuealpha.CapacityProvider
		cm *corev1.ConfigMap
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)
		ns = utiltesting.MakeNamespaceWithGenerateName("cp-")
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		if cp != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
		}
		if cm != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cm, true)
		}
	})

	ginkgo.It("Should synchronize capacity from ConfigMap and dynamically handle updates and deletions", func() {
		cm = capacityprovidertest.MakeCapacityConfigMapWithGenerateName("test-capacity-cm-", metav1.NamespaceDefault).
			Flavor("f1", corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10"),
				corev1.ResourceMemory: resource.MustParse("50Gi"),
			}).
			Flavor("f2", corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("20"),
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cm)

		cp = utiltestingalpha.MakeCapacityProviderWithGenerateName("test-cp-").
			ControllerName(capacityprovidertest.TestCapacityProviderControllerName).
			OrchestratedFlavors("f1", "f2").
			Parameters("k8s.io", "ConfigMap", cm.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, cp)

		cpKey := types.NamespacedName{Name: cp.Name}
		createdCp := &kueuealpha.CapacityProvider{}

		ginkgo.By("Verifying initial capacity sync", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cpKey, createdCp)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(createdCp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).Should(gomega.Equal(kueuealpha.CapacityProviderReasonSynchronized))

				wantCapacity := utiltestingalpha.MakeNormalizedCapacity().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10"),
						corev1.ResourceMemory: resource.MustParse("50Gi"),
					}).
					Flavor("f2", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("20"),
					}).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, createdCp.Status.Capacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating ConfigMap and verifying dynamic CapacityProvider update", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestCM corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: cm.Name}, &latestCM)).Should(gomega.Succeed())
				latestCM.Data[capacityprovidertest.CapacityConfigMapKey] = capacityprovidertest.MakeCapacityConfig().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("15"),
						corev1.ResourceMemory: resource.MustParse("50Gi"),
					}).
					Flavor("f2", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("20"),
					}).
					MustMarshal()
				g.Expect(k8sClient.Update(ctx, &latestCM)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cpKey, createdCp)).Should(gomega.Succeed())
				wantUpdatedCapacity := utiltestingalpha.MakeNormalizedCapacity().
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("15"),
						corev1.ResourceMemory: resource.MustParse("50Gi"),
					}).
					Flavor("f2", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("20"),
					}).
					Obj()
				g.Expect(cmp.Diff(wantUpdatedCapacity, createdCp.Status.Capacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting ConfigMap and verifying CapacitySynchronized=False (Misconfigured)", func() {
			var latestCM corev1.ConfigMap
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: cm.Name}, &latestCM)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, &latestCM)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cpKey, createdCp)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(createdCp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).Should(gomega.Equal(kueuealpha.CapacityProviderReasonMisconfigured))
				g.Expect(createdCp.Status.Capacity).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
