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
	"context"

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
		cohort       *kueue.Cohort
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)

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
		cohort = nil
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		if cohort != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		}
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
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")
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
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
		})

		ginkgo.By("clearing EffectiveQuota in status", func() {
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "", "")
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
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, false)

		updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")

		wl := utiltestingapi.MakeWorkload("wl-fg-disabled", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
	})

	ginkgo.It("should adjust scheduling capacity dynamically when EffectiveQuota is reduced", func() {
		ginkgo.By("setting initial EffectiveQuota of 10 CPU", func() {
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "10", "dqo-test")
		})

		wl1 := utiltestingapi.MakeWorkload("wl1-reduction", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, wl1)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

		ginkgo.By("reducing EffectiveQuota to 5 CPU while wl1 is running", func() {
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")
		})

		ginkgo.By("verifying a new workload exceeding reduced EffectiveQuota stays pending", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2-reduction", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	})

	ginkgo.It("should report effective quota in cluster queue info metric", func() {
		ginkgo.By("verifying initial info metric has_effective_quota='false'", func() {
			util.ExpectClusterQueueInfoWithEffectiveQuotaMetric(clusterQueue.Name, "", "", "false", "", 1)
		})

		ginkgo.By("updating ClusterQueue status with EffectiveQuota providing 10 CPU and manager 'dqo-mgr'", func() {
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "10", "dqo-mgr")
		})

		ginkgo.By("verifying info metric reports has_effective_quota='true' and manager name", func() {
			util.ExpectClusterQueueInfoWithEffectiveQuotaMetric(clusterQueue.Name, "", "", "true", "dqo-mgr", 1)
		})

		ginkgo.By("clearing EffectiveQuota in status", func() {
			updateCQEffectiveQuota(ctx, k8sClient, clusterQueue, flavor, "", "")
		})

		ginkgo.By("verifying info metric reverts to has_effective_quota='false'", func() {
			util.ExpectClusterQueueInfoWithEffectiveQuotaMetric(clusterQueue.Name, "", "", "false", "", 1)
		})
	})

	ginkgo.It("should report effective quota in cohort info metric", func() {
		cohort = utiltestingapi.MakeCohort("dynquota-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "0").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cohort)

		ginkgo.By("verifying initial cohort info metric has_effective_quota='false'", func() {
			util.ExpectCohortInfoWithEffectiveQuotaMetric(cohort.Name, "", cohort.Name, "false", "", 1)
		})

		ginkgo.By("updating Cohort status with EffectiveQuota", func() {
			updateCohortEffectiveQuota(ctx, k8sClient, cohort, flavor, "20", "dqo-cohort-mgr")
		})

		ginkgo.By("verifying cohort info metric reports has_effective_quota='true' and manager name", func() {
			util.ExpectCohortInfoWithEffectiveQuotaMetric(cohort.Name, "", cohort.Name, "true", "dqo-cohort-mgr", 1)
		})

		ginkgo.By("clearing Cohort EffectiveQuota in status", func() {
			updateCohortEffectiveQuota(ctx, k8sClient, cohort, flavor, "", "")
		})

		ginkgo.By("verifying cohort info metric reverts to has_effective_quota='false'", func() {
			util.ExpectCohortInfoWithEffectiveQuotaMetric(cohort.Name, "", cohort.Name, "false", "", 1)
		})
	})
})

func updateCQEffectiveQuota(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, flavor *kueue.ResourceFlavor, cpuQty, managerName string) {
	gomega.Eventually(func(g gomega.Gomega) {
		var currentCQ kueue.ClusterQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &currentCQ)).To(gomega.Succeed())
		currentCQ.Status.EffectiveQuota = makeEffectiveQuotaStatus(flavor, cpuQty, managerName)
		g.Expect(k8sClient.Status().Update(ctx, &currentCQ)).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func updateCohortEffectiveQuota(ctx context.Context, k8sClient client.Client, cohort *kueue.Cohort, flavor *kueue.ResourceFlavor, cpuQty, managerName string) {
	gomega.Eventually(func(g gomega.Gomega) {
		var currentCohort kueue.Cohort
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), &currentCohort)).To(gomega.Succeed())
		currentCohort.Status.EffectiveQuota = makeEffectiveQuotaStatus(flavor, cpuQty, managerName)
		g.Expect(k8sClient.Status().Update(ctx, &currentCohort)).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func makeEffectiveQuotaStatus(flavor *kueue.ResourceFlavor, cpuQty, managerName string) *kueue.EffectiveQuotaStatus {
	if cpuQty == "" {
		return nil
	}
	return &kueue.EffectiveQuotaStatus{
		LastUpdateTime: metav1.Now(),
		ManagerRef: kueue.EffectiveQuotaStatusManagerRef{
			APIGroup: "kueue.x-k8s.io",
			Kind:     "DynamicQuotaOrchestrator",
			Name:     managerName,
		},
		ResourceGroups: []kueue.ResourceGroup{
			utiltestingapi.ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, cpuQty).
					Obj(),
			),
		},
	}
}
