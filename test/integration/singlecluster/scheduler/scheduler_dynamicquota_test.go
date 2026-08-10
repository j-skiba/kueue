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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler DynamicQuota", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuota, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dynquota-")

		flavor = utiltestingapi.MakeResourceFlavor("dynquota-flavor").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("dynquota-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "0").
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("dynquota-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should admit pending workloads when EffectiveQuota status is updated with quota", func() {
		wl := utiltestingapi.MakeWorkload("wl1", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("creating a workload when spec quota is 0", func() {
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		})

		ginkgo.By("updating ClusterQueue status with EffectiveQuota providing 5 CPU", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var cq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Status.EffectiveQuota = &kueue.EffectiveQuotaStatus{
					LastUpdateTime: metav1.Now(),
					ManagerRef: kueue.EffectiveQuotaStatusManagerRef{
						Kind: "DynamicQuotaOrchestrator",
						Name: "dqo-test",
					},
					ResourceGroups: []kueue.ResourceGroup{
						utiltestingapi.ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(flavor.Name).
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						),
					},
				}
				g.Expect(k8sClient.Status().Update(ctx, &cq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the pending workload is scheduled and admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("creating a second workload that fits within remaining EffectiveQuota", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
		})

		ginkgo.By("creating a third workload that exceeds remaining EffectiveQuota", func() {
			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
		})
	})

	ginkgo.It("should fallback to spec when EffectiveQuota status is cleared", func() {
		ginkgo.By("populating EffectiveQuota to allow initial admission", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var cq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Status.EffectiveQuota = &kueue.EffectiveQuotaStatus{
					LastUpdateTime: metav1.Now(),
					ManagerRef: kueue.EffectiveQuotaStatusManagerRef{
						Kind: "DynamicQuotaOrchestrator",
						Name: "dqo-test",
					},
					ResourceGroups: []kueue.ResourceGroup{
						utiltestingapi.ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(flavor.Name).
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						),
					},
				}
				g.Expect(k8sClient.Status().Update(ctx, &cq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
		})

		ginkgo.By("clearing EffectiveQuota in status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var cq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Status.EffectiveQuota = nil
				g.Expect(k8sClient.Status().Update(ctx, &cq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying subsequent workload falls back to spec and stays pending", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	})

	ginkgo.It("should ignore EffectiveQuota status when DynamicQuota feature gate is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuota, false)

		gomega.Eventually(func(g gomega.Gomega) {
			var cq kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
			cq.Status.EffectiveQuota = &kueue.EffectiveQuotaStatus{
				LastUpdateTime: metav1.Now(),
				ManagerRef: kueue.EffectiveQuotaStatusManagerRef{
					Kind: "DynamicQuotaOrchestrator",
					Name: "dqo-test",
				},
				ResourceGroups: []kueue.ResourceGroup{
					utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					),
				},
			}
			g.Expect(k8sClient.Status().Update(ctx, &cq)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wl := utiltestingapi.MakeWorkload("wl-fg-disabled", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
	})
})
