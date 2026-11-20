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

package queue

import (
	"sync"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
)

type cqKeyed interface {
	comparable
	clusterQueue() kueue.ClusterQueueReference
}

type workloadStatus[C cqKeyed] interface {
	comparable
	cqStatus() C
}

// cqStatusTracker encapsulates internal state for tracking per-workload statuses
// and their aggregated metric counts per ClusterQueue.
//
// It is not thread-safe: callers MUST hold their own lock for all *Locked methods
// (write lock for mutations, at least read lock for resyncCQMetricsLocked) and
// MUST hold at least m.RLock() so reading m.hm.ClusterQueue(...) and
// m.customLabels.CQGet(...) is race-free.
type cqStatusTracker[S workloadStatus[C], C cqKeyed] struct {
	// statuses maps each currently tracked workload to its last recorded status S,
	// so updates can skip no-ops and decrement the previous bucket on transitions.
	statuses map[workload.Reference]S
	// perCQ holds the current workload count for each ClusterQueue metric label set C,
	// used to update, clear, and resync the ClusterQueue gauge.
	perCQ map[C]int

	metricName string
	report     func(C, []string, *roletracker.RoleTracker, int)
	clear      func(C, []string, *roletracker.RoleTracker)
}

func newCQStatusTracker[S workloadStatus[C], C cqKeyed](
	metricName string,
	report func(C, []string, *roletracker.RoleTracker, int),
	clearFn func(C, []string, *roletracker.RoleTracker),
) *cqStatusTracker[S, C] {
	return &cqStatusTracker[S, C]{
		statuses:   make(map[workload.Reference]S),
		perCQ:      make(map[C]int),
		metricName: metricName,
		report:     report,
		clear:      clearFn,
	}
}

// setLocked records the workload's status and updates the ClusterQueue metric.
// Returns the displaced status (if any) and whether the tracked status changed.
func (t *cqStatusTracker[S, C]) setLocked(
	log logr.Logger,
	wlKey workload.Reference,
	newStatus S,
	m *Manager,
) (S, bool, bool) {
	oldStatus, hadOld := t.statuses[wlKey]
	if hadOld && oldStatus == newStatus {
		log.V(5).Info("Workload metric status unchanged, skipping update", "metric", t.metricName, "workload", wlKey, "status", newStatus)
		return oldStatus, true, false
	}

	log.V(4).Info("Updating workload metric tracking", "metric", t.metricName, "workload", wlKey, "oldStatus", oldStatus, "newStatus", newStatus)

	if hadOld {
		t.applyDeltaLocked(log, oldStatus, -1, m)
	}
	t.statuses[wlKey] = newStatus
	t.applyDeltaLocked(log, newStatus, 1, m)
	return oldStatus, hadOld, true
}

// removeLocked clears a workload from tracking, updates the ClusterQueue metric,
// and returns the removed status and true iff the workload was tracked.
func (t *cqStatusTracker[S, C]) removeLocked(
	log logr.Logger,
	wlKey workload.Reference,
	m *Manager,
) (S, bool) {
	oldStatus, ok := t.statuses[wlKey]
	if !ok {
		log.V(4).Info("Workload to remove was not tracked", "metric", t.metricName, "workload", wlKey)
		return oldStatus, false
	}
	log.V(4).Info("Removing workload from metric tracking", "metric", t.metricName, "workload", wlKey, "status", oldStatus)
	t.applyDeltaLocked(log, oldStatus, -1, m)
	delete(t.statuses, wlKey)
	return oldStatus, true
}

func (t *cqStatusTracker[S, C]) applyDeltaLocked(
	log logr.Logger,
	status S,
	diff int,
	m *Manager,
) {
	cqKey := status.cqStatus()
	cqName := cqKey.clusterQueue()
	t.perCQ[cqKey] += diff
	count := t.perCQ[cqKey]

	if count <= 0 {
		delete(t.perCQ, cqKey)
	}
	// The status may belong to a deleted ClusterQueue, whose series must not be reported again.
	// Callers hold at least the Manager RLock, so reading the ClusterQueues is safe.
	if cqName != "" && m.hm.ClusterQueue(cqName) != nil {
		cqCustomLabels := m.customLabels.CQGet(cqName)
		if count <= 0 {
			log.V(4).Info("Clearing CQ workload metric", "metric", t.metricName, "clusterQueue", cqName, "status", cqKey)
			t.clear(cqKey, cqCustomLabels, m.roleTracker)
		} else {
			log.V(4).Info("Reporting CQ workload metric", "metric", t.metricName, "clusterQueue", cqName, "status", cqKey, "count", count)
			t.report(cqKey, cqCustomLabels, m.roleTracker, count)
		}
	} else {
		log.V(4).Info("Skipping CQ workload metric report due to missing ClusterQueue", "metric", t.metricName, "status", status)
	}
}

// resyncCQMetricsLocked re-reports all tracked counts for cqName.
func (t *cqStatusTracker[S, C]) resyncCQMetricsLocked(cqName kueue.ClusterQueueReference, m *Manager) {
	for cqKey, count := range t.perCQ {
		if cqKey.clusterQueue() == cqName {
			cqCustomLabels := m.customLabels.CQGet(cqName)
			t.report(cqKey, cqCustomLabels, m.roleTracker, count)
		}
	}
}

type unadmittedWorkloadStatus struct {
	ClusterQueue        kueue.ClusterQueueReference
	LocalQueueName      kueue.LocalQueueName
	LocalQueueNamespace string
	Reason              string
	UnderlyingCause     string
}

type unadmittedCQStatus struct {
	ClusterQueue    kueue.ClusterQueueReference
	Reason          string
	UnderlyingCause string
}

func (s unadmittedCQStatus) clusterQueue() kueue.ClusterQueueReference {
	return s.ClusterQueue
}

func (s unadmittedWorkloadStatus) ClusterQueueStatus() unadmittedCQStatus {
	return unadmittedCQStatus{
		ClusterQueue:    s.ClusterQueue,
		Reason:          s.Reason,
		UnderlyingCause: s.UnderlyingCause,
	}
}

func (s unadmittedWorkloadStatus) cqStatus() unadmittedCQStatus {
	return s.ClusterQueueStatus()
}

// unadmittedWorkloads encapsulates internal state for tracking unadmitted workloads
// and their metric counts per ClusterQueue and LocalQueue.
type unadmittedWorkloads struct {
	rwm sync.RWMutex
	*cqStatusTracker[unadmittedWorkloadStatus, unadmittedCQStatus]
	perLQ map[unadmittedWorkloadStatus]int
}

func newUnadmittedWorkloads() *unadmittedWorkloads {
	return &unadmittedWorkloads{
		cqStatusTracker: newCQStatusTracker[unadmittedWorkloadStatus, unadmittedCQStatus](
			"unadmitted",
			func(s unadmittedCQStatus, customLabels []string, tracker *roletracker.RoleTracker, count int) {
				metrics.ReportUnadmittedWorkload(s.ClusterQueue, s.Reason, s.UnderlyingCause, customLabels, tracker, count)
			},
			func(s unadmittedCQStatus, customLabels []string, tracker *roletracker.RoleTracker) {
				metrics.ClearUnadmittedWorkloadLabelValues(s.ClusterQueue, s.Reason, s.UnderlyingCause, customLabels, tracker)
			},
		),
		perLQ: make(map[unadmittedWorkloadStatus]int),
	}
}

func (u *unadmittedWorkloads) update(
	log logr.Logger,
	wl *kueue.Workload,
	cqName kueue.ClusterQueueReference,
	m *Manager,
) {
	u.rwm.Lock()
	defer u.rwm.Unlock()

	log = log.WithValues("workload", klog.KObj(wl))
	status := getUnadmittedWorkloadStatus(log, wl)
	wlKey := workload.Key(wl)
	if status == nil {
		if oldStatus, ok := u.removeLocked(log, wlKey, m); ok {
			u.updateLQMetricLocked(log, oldStatus, -1, m)
		}
		return
	}
	if cqName != "" {
		status.ClusterQueue = cqName
	} else {
		log.V(3).Info("Failed to resolve ClusterQueue for unadmitted workload", "queue", wl.Spec.QueueName)
	}
	oldStatus, hadOld, changed := u.setLocked(log, wlKey, *status, m)
	if !changed {
		return
	}
	if hadOld {
		u.updateLQMetricLocked(log, oldStatus, -1, m)
	}
	u.updateLQMetricLocked(log, *status, 1, m)
}

func (u *unadmittedWorkloads) remove(
	log logr.Logger,
	wlKey workload.Reference,
	m *Manager,
) {
	u.rwm.Lock()
	defer u.rwm.Unlock()

	if oldStatus, ok := u.removeLocked(log, wlKey, m); ok {
		u.updateLQMetricLocked(log, oldStatus, -1, m)
	}
}

func (u *unadmittedWorkloads) resyncCQMetrics(cqName kueue.ClusterQueueReference, m *Manager) {
	u.rwm.RLock()
	defer u.rwm.RUnlock()

	u.resyncCQMetricsLocked(cqName, m)
}

func (u *unadmittedWorkloads) updateLQMetricLocked(
	log logr.Logger,
	status unadmittedWorkloadStatus,
	diff int,
	m *Manager,
) {
	if !m.lqMetrics.IsEnabled() {
		return
	}
	u.perLQ[status] += diff
	countLQ := u.perLQ[status]

	if countLQ <= 0 {
		delete(u.perLQ, status)
	}

	lqRefKey := queue.NewLocalQueueReference(status.LocalQueueNamespace, status.LocalQueueName)
	// The status may name a LocalQueue that was deleted or that the Workload has left.
	lqExists := m.LocalQueueExistsWithoutLock(lqRefKey)
	if lqExists && status.ClusterQueue != "" {
		lqRef := metrics.LocalQueueReference{Name: status.LocalQueueName, Namespace: status.LocalQueueNamespace}
		lqCustomLabels := m.customLabels.LQGet(lqRefKey)
		if countLQ <= 0 {
			log.V(4).Info("Clearing LQ unadmitted workload metric", "localQueue", lqRefKey, "status", status)
			metrics.ClearLocalQueueUnadmittedWorkloadLabelValues(
				lqRef,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
			)
		} else {
			log.V(4).Info("Reporting LQ unadmitted workload metric", "localQueue", lqRefKey, "status", status, "count", countLQ)
			metrics.ReportLocalQueueUnadmittedWorkload(
				lqRef,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
				countLQ,
			)
		}
	} else {
		log.V(4).Info("Skipping LQ unadmitted metric report", "status", status, "qExists", lqExists)
	}
}

func (u *unadmittedWorkloads) resyncLQMetrics(lqRef queue.LocalQueueReference, m *Manager) {
	u.rwm.RLock()
	defer u.rwm.RUnlock()

	lqNamespace, lqName := queue.MustParseLocalQueueReference(lqRef)
	for status, count := range u.perLQ {
		if status.ClusterQueue != "" && status.LocalQueueNamespace == lqNamespace && status.LocalQueueName == lqName {
			lqCustomLabels := m.customLabels.LQGet(lqRef)
			lqRefKey := metrics.LocalQueueReference{Name: lqName, Namespace: lqNamespace}
			metrics.ReportLocalQueueUnadmittedWorkload(
				lqRefKey,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
				count,
			)
		}
	}
}

func getUnadmittedWorkloadStatus(log logr.Logger, wl *kueue.Workload) *unadmittedWorkloadStatus {
	if concurrentadmission.IsVariant(wl) {
		log.V(5).Info("Ignoring variant workload for unadmitted metrics tracking", "workload", klog.KObj(wl))
		return nil
	}

	if workload.IsAdmitted(wl) || workloadfinish.IsFinished(wl) || !wl.DeletionTimestamp.IsZero() {
		log.V(4).Info("Workload is not unadmitted",
			"isAdmitted", workload.IsAdmitted(wl),
			"isFinished", workloadfinish.IsFinished(wl),
			"isDeleting", !wl.DeletionTimestamp.IsZero(),
		)
		return nil
	}

	admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)

	var reason string
	if admittedCond != nil {
		reason = admittedCond.Reason
	} else {
		// If the UnadmittedWorkloadsExplicitStatus feature gate is disabled, a newly
		// created workload has no status conditions. Fall back to NoReservation.
		reason = kueue.WorkloadAdmittedReasonNoReservation
	}
	quotaReservedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	underlyingCause := resolveUnadmittedUnderlyingCause(quotaReservedCond)

	return &unadmittedWorkloadStatus{
		LocalQueueName:      wl.Spec.QueueName,
		LocalQueueNamespace: wl.Namespace,
		Reason:              reason,
		UnderlyingCause:     underlyingCause,
	}
}

func resolveUnadmittedUnderlyingCause(quotaReservedCond *metav1.Condition) string {
	if quotaReservedCond == nil {
		// If the scheduler has not evaluated the workload yet, or if the UnadmittedWorkloadsExplicitStatus
		// feature gate is disabled (meaning conditions are not populated), the QuotaReserved condition
		// will be missing. We assume the workload is pending evaluation.
		return kueue.WorkloadQuotaReservedReasonPendingEvaluation
	}
	if quotaReservedCond.Status == metav1.ConditionTrue {
		return ""
	}
	return quotaReservedCond.Reason
}
