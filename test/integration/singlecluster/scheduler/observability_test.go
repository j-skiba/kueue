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
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload Unadmitted Observability Status Logic", func() {
	var (
		ns                *corev1.Namespace
		onDemandFlavor    *kueue.ResourceFlavor
		spotTaintedFlavor *kueue.ResourceFlavor
		cqs               []*kueue.ClusterQueue
		customFlavors     []*kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsObservability, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsExplicitStatus, true)
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
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range cqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		cqs = nil
		for _, rf := range customFlavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		}
		customFlavors = nil
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
	})

	ginkgo.It("Should set WaitingForQuota status if one flavor has insufficient quota but another is misconfigured", func() {
		cq := utiltestingapi.MakeClusterQueue("pending-capacity-cq").
			Cohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "2", "6").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2", "6").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		siblingCQ := utiltestingapi.MakeClusterQueue("cq-sibling").
			Cohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4", "6").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, siblingCQ)
		cqs = append(cqs, siblingCQ)

		lq := utiltestingapi.MakeLocalQueue("pending-capacity-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		siblingLQ := utiltestingapi.MakeLocalQueue("sibling-q", ns.Name).ClusterQueue(siblingCQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, siblingLQ)

		// 1. Submit a pre-existing job to consume 4 CPUs on on-demand flavor in the sibling queue (so cohort capacity is 4/6 used)
		existingJob := utiltestingapi.MakeWorkload("existing-job", ns.Name).
			Queue(kueue.LocalQueueName(siblingLQ.Name)).
			Request(corev1.ResourceCPU, "4").Obj()
		util.MustCreate(ctx, k8sClient, existingJob)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)

		// 2. Submit a new job requesting 3 CPUs in our queue
		// - spot-tainted is misconfigured (taints mismatch, structural)
		// - on-demand has nominal 2, max 6, cohort has remaining 2 (6-4). Since request is 3, cohort lacks sufficient remaining quota (capacity wait)
		newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "3").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		// The job should be left pending with Reason = WaitingForQuota!
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set ExceedsMaxQuota status if one flavor has taint mismatch but another exceeds limits", func() {
		cq := utiltestingapi.MakeClusterQueue("not-enough-quota-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("not-enough-quota-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Submit a new job requesting 10 CPUs
		// - spot-tainted is structurally mismatched (taints mismatch)
		// - on-demand exceeds max capacity limits (5)
		newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "10").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		// The job should be left pending with Reason = ExceedsMaxQuota
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set NoMatchingFlavor status if both flavors are structurally incompatible", func() {
		// Create a second tainted flavor so that both flavors in the CQ are structurally incompatible
		taintedFlavor := utiltestingapi.MakeResourceFlavor("tainted-on-demand").
			Taint(corev1.Taint{
				Key:    "key",
				Value:  "val2",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
		util.MustCreate(ctx, k8sClient, taintedFlavor)
		customFlavors = append(customFlavors, taintedFlavor)

		cq := utiltestingapi.MakeClusterQueue("misconfigured-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
				*utiltestingapi.MakeFlavorQuotas("tainted-on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("misconfigured-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Submit a new job requesting 1 CPU
		// - spot-tainted is structurally mismatched (taint mismatch)
		// - tainted-on-demand is structurally mismatched (taint mismatch)
		newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		// The job should be left pending with Reason = NoMatchingFlavor!
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonNoMatchingFlavor))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should initialize conditions to False with PendingEvaluation and NoReservation on first cycle", func() {
		cq := utiltestingapi.MakeClusterQueue("pending-evaluation-cq").
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("pending-evaluation-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Wait for CQ to be active/cached
		gomega.Eventually(func(g gomega.Gomega) {
			var createdCQ kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Wait for LQ status condition to be synchronized
		gomega.Eventually(func(g gomega.Gomega) {
			var createdLQ kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdLQ.Status.Conditions, kueue.LocalQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// 1. Submit existing-job to consume 1 CPU
		existingJob := utiltestingapi.MakeWorkload("existing-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, existingJob)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)

		// 2. Submit blocked-job (will wait for capacity -> WaitingForQuota)
		blockedJob := utiltestingapi.MakeWorkload("blocked-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, blockedJob)

		// Wait until blocked-job gets WaitingForQuota status
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockedJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// 3. Submit new-job (blocked behind blocked-job in StrictFIFO -> remains PendingEvaluation!)
		newJob := utiltestingapi.MakeWorkload("new-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			condReserved := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(condReserved).NotTo(gomega.BeNil())
			g.Expect(string(condReserved.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(condReserved.Reason).To(gomega.BeElementOf(
				kueue.WorkloadQuotaReservedReasonPendingEvaluation,
				kueue.WorkloadQuotaReservedReasonWaitingForQuota,
			))

			condAdmitted := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
			g.Expect(condAdmitted).NotTo(gomega.BeNil())
			g.Expect(string(condAdmitted.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(condAdmitted.Reason).To(gomega.Equal("NoReservation"))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set Misconfigured status if referencing non-existent LocalQueue", func() {
		newJob := utiltestingapi.MakeWorkload("new-job-missing-q", ns.Name).
			Queue("non-existent-q").
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			condReserved := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(condReserved).NotTo(gomega.BeNil())
			g.Expect(string(condReserved.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(condReserved.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonMisconfigured))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set Suspended status if referencing stopped LocalQueue", func() {
		cq := utiltestingapi.MakeClusterQueue("suspended-lq-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("suspended-lq-q", ns.Name).
			ClusterQueue(cq.Name).
			StopPolicy(kueue.HoldAndDrain).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Wait for CQ to be active/cached
		gomega.Eventually(func(g gomega.Gomega) {
			var createdCQ kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Wait for LQ status condition to be synchronized
		gomega.Eventually(func(g gomega.Gomega) {
			var createdLQ kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdLQ.Status.Conditions, kueue.LocalQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		newJob := utiltestingapi.MakeWorkload("new-job-stopped-q", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			condReserved := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(condReserved).NotTo(gomega.BeNil())
			g.Expect(string(condReserved.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(condReserved.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonSuspended))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should correctly report and prune unadmitted workload metrics", func() {
		cq := utiltestingapi.MakeClusterQueue("metrics-cq").
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("metrics-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Wait for CQ and LQ status conditions to be synchronized
		gomega.Eventually(func(g gomega.Gomega) {
			var createdCQ kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			var createdLQ kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdLQ.Status.Conditions, kueue.LocalQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// 1. Submit existing-job to consume 1 CPU -> will be admitted
		existingJob := utiltestingapi.MakeWorkload("existing-job-metrics", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, existingJob)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)

		// 2. Submit a pending job (will stay pending with WaitingForQuota status)
		pendingJob := utiltestingapi.MakeWorkload("pending-job-metrics", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, pendingJob)

		// Wait for pending-job to reach WaitingForQuota status
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Verify the metrics are reported:
		gomega.Eventually(func(g gomega.Gomega) {
			metric := metrics.UnadmittedWorkloads.WithLabelValues(
				cq.Name,
				"NoReservation",
				kueue.WorkloadQuotaReservedReasonWaitingForQuota,
				roletracker.RoleStandalone,
			)
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(v).To(gomega.Equal(float64(1)))

			metricLQ := metrics.LocalQueueUnadmittedWorkloads.WithLabelValues(
				lq.Name,
				ns.Name,
				cq.Name,
				"NoReservation",
				kueue.WorkloadQuotaReservedReasonWaitingForQuota,
				roletracker.RoleStandalone,
			)
			vLQ, errLQ := testutil.GetGaugeMetricValue(metricLQ)
			g.Expect(errLQ).NotTo(gomega.HaveOccurred())
			g.Expect(vLQ).To(gomega.Equal(float64(1)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// 3. Delete the pending job
		gomega.Expect(k8sClient.Delete(ctx, pendingJob)).To(gomega.Succeed())

		// Verify the metrics series is completely PRUNED (deleted from registry) once count drops to 0!
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.UnadmittedWorkloads, map[string]string{
				"cluster_queue":    cq.Name,
				"reason":           "NoReservation",
				"underlying_cause": kueue.WorkloadQuotaReservedReasonWaitingForQuota,
			})).To(gomega.BeEmpty())

			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueUnadmittedWorkloads, map[string]string{
				"name":             lq.Name,
				"namespace":        ns.Name,
				"cluster_queue":    cq.Name,
				"reason":           "NoReservation",
				"underlying_cause": kueue.WorkloadQuotaReservedReasonWaitingForQuota,
			})).To(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not initialize conditions to False on first cycle if UnadmittedWorkloadsExplicitStatus is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsExplicitStatus, false)

		newJob := utiltestingapi.MakeWorkload("new-job-no-explicit-status", ns.Name).
			Queue("non-existent-q").
			Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, newJob)

		gomega.Consistently(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
			g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)).To(gomega.BeNil())
			g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeNil())
		}, util.ShortTimeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			metric := metrics.UnadmittedWorkloads.WithLabelValues(
				"",
				"NoReservation",
				kueue.WorkloadQuotaReservedReasonPendingEvaluation,
				roletracker.RoleStandalone,
			)
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(v).To(gomega.Equal(float64(1)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set PendingPreemption status when workload requires preemption and triggers it", func() {
		cq := utiltestingapi.MakeClusterQueue("preemption-cq").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue("preemption-q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		// Wait for CQ and LQ status conditions to be synchronized
		gomega.Eventually(func(g gomega.Gomega) {
			var createdCQ kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			var createdLQ kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(createdLQ.Status.Conditions, kueue.LocalQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// 1. Submit a low priority job to consume all CPU (4)
		lowPriorityJob := utiltestingapi.MakeWorkload("low-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "4").
			Priority(-10).Obj()
		util.MustCreate(ctx, k8sClient, lowPriorityJob)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriorityJob)

		// 2. Submit a high priority job that requires preemption (needs 2 CPUs, but CQ is at 4/4)
		highPriorityJob := utiltestingapi.MakeWorkload("high-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "2").
			Priority(10).Obj()
		util.MustCreate(ctx, k8sClient, highPriorityJob)

		// The high priority job should trigger preemption, and during the preemption transition,
		// its status should be set to PendingPreemption.
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highPriorityJob), &wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(string(cond.Status)).To(gomega.Equal(string(metav1.ConditionFalse)))
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonPendingPreemption))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
