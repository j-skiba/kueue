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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload Unadmitted Observability Status Logic", func() {
	var (
		ns                *corev1.Namespace
		onDemandFlavor    *kueue.ResourceFlavor
		spotTaintedFlavor *kueue.ResourceFlavor
		cqs               []*kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "obs-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotTaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-tainted").
			Taint(corev1.Taint{
				Key:    "key",
				Value:  "val",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
		util.MustCreate(ctx, k8sClient, spotTaintedFlavor)
	})

	ginkgo.AfterEach(func() {
		// 1. Delete namespace first to delete all workloads and release their finalizers / active quota holds!
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())

		// 2. Safely delete the cluster queues without blocks!
		for _, cq := range cqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		cqs = nil

		// 3. Delete resources flavors
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
	})

	ginkgo.When("Evaluating multi-flavor resource assignments with status priorities", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadUnadmittedObservability, true)
		})

		ginkgo.It("Should set PendingCapacity status if one flavor has insufficient quota but another is misconfigured", func() {
			cq := utiltestingapi.MakeClusterQueue("pending-capacity-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "15").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("pending-capacity-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			// 1. Submit a pre-existing job to consume 10 CPUs on on-demand flavor (it fits immediately)
			existingJob := utiltestingapi.MakeWorkload("existing-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "10").Obj()
			util.MustCreate(ctx, k8sClient, existingJob)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)

			// 2. Submit a new job requesting 10 CPUs (fails spot-tainted due to taint mismatch, and on-demand due to remaining quota = 5)
			newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "10").Obj()
			util.MustCreate(ctx, k8sClient, newJob)

			// 3. The new job should be left pending with Reason = PendingCapacity!
			gomega.Eventually(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonPendingCapacity))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should set Misconfigured status if both flavors are misconfigured", func() {
			cq := utiltestingapi.MakeClusterQueue("misconfigured-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("misconfigured-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			// Submit a new job requesting 10 CPUs (fails spot-tainted due to taint mismatch, and on-demand due to exceeds max capacity limits 5)
			newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "10").Obj()
			util.MustCreate(ctx, k8sClient, newJob)

			// The job should be left pending with Reason = Misconfigured!
			gomega.Eventually(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonMisconfigured))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
