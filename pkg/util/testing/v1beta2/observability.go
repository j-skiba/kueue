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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// GetPendingReason returns the expected condition reason for a pending workload,
// depending on whether the UnadmittedWorkloadsObservability feature is enabled.
func GetPendingReason() string {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		return string(kueue.WorkloadQuotaReservedReasonPendingEvaluation)
	}
	return "Pending"
}

// GetDeactivatedReason returns the expected condition reason for a deactivated workload,
// depending on whether the UnadmittedWorkloadsObservability feature is enabled.
func GetDeactivatedReason() string {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		return kueue.WorkloadQuotaReservedReasonDeactivated
	}
	return "Pending"
}

// GetPendingMessage returns the expected condition message for a pending workload,
// depending on whether the UnadmittedWorkloadsObservability feature is enabled.
func GetPendingMessage(message string) string {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		return "The workload is pending evaluation"
	}
	return message
}

// GetDeactivatedMessage returns the expected condition message for a deactivated workload,
// depending on whether the UnadmittedWorkloadsObservability feature is enabled.
func GetDeactivatedMessage(message string) string {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		return "The workload is deactivated"
	}
	return message
}

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
			Reason:             "NoReservation",
			Message:            "The workload has no reservation",
			LastTransitionTime: metav1.NewTime(now),
		},
	}
}

// AdjustWorkloadsConditionsForObservability adjusts the expected conditions in wantWorkloads
// based on whether UnadmittedWorkloadsExplicitStatus is enabled and if wantConditionsObservability
// is provided. If wantConditionsObservability is not provided, it falls back to borrowing the
// actual conditions from gotWorkloads for workloads that are expected to be unadmitted.
func AdjustWorkloadsConditionsForObservability(
	wantWorkloads []kueue.Workload,
	gotWorkloads []kueue.Workload,
	wantConditionsObservability map[string][]metav1.Condition,
) {
	if !features.Enabled(features.UnadmittedWorkloadsExplicitStatus) {
		return
	}
	for i := range wantWorkloads {
		wantWl := &wantWorkloads[i]
		if len(wantConditionsObservability) > 0 {
			if conds, ok := wantConditionsObservability[wantWl.Name]; ok {
				for _, cond := range conds {
					apimeta.SetStatusCondition(&wantWl.Status.Conditions, cond)
				}
			}
		} else {
			var gotWl *kueue.Workload
			for j := range gotWorkloads {
				if gotWorkloads[j].Name == wantWl.Name && gotWorkloads[j].Namespace == wantWl.Namespace {
					gotWl = &gotWorkloads[j]
					break
				}
			}
			if gotWl != nil {
				hasQuotaReservedFalse := false
				for _, c := range wantWl.Status.Conditions {
					if c.Type == kueue.WorkloadQuotaReserved && c.Status == metav1.ConditionFalse {
						hasQuotaReservedFalse = true
						break
					}
				}
				if hasQuotaReservedFalse {
					wantWl.Status.Conditions = gotWl.Status.Conditions
				}
			}
		}
	}
}
