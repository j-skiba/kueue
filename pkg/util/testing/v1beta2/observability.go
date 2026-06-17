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

package v1beta2

import (
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// GetObservabilityConditions returns the status conditions that are set on an unadmitted workload
// when the UnadmittedWorkloadsObservability and UnadmittedWorkloadsExplicitStatus feature gates are enabled.
func GetObservabilityConditions(reason, message string, now time.Time) []metav1.Condition {
	return []metav1.Condition{
		{
			Type:               kueue.WorkloadQuotaReserved,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: metav1.NewTime(now),
		},
		{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionFalse,
			Reason:             kueue.WorkloadAdmittedReasonNoReservation,
			Message:            "The workload has no reservation",
			LastTransitionTime: metav1.NewTime(now),
		},
	}
}

// ApplyStatusConditions applies the given conditions to the workloads with matching names.
func ApplyStatusConditions(
	workloads []kueue.Workload,
	conditions map[string][]metav1.Condition,
) {
	for i := range workloads {
		wl := &workloads[i]
		namespacedName := types.NamespacedName{Namespace: wl.Namespace, Name: wl.Name}.String()
		if conds, ok := conditions[namespacedName]; ok {
			for _, cond := range conds {
				apimeta.SetStatusCondition(&wl.Status.Conditions, cond)
			}
		} else if conds, ok := conditions[wl.Name]; ok {
			for _, cond := range conds {
				apimeta.SetStatusCondition(&wl.Status.Conditions, cond)
			}
		}
	}
}
