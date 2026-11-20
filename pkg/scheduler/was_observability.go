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
	"errors"

	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
)

const (
	wasEventReasonIncompatibleFlavor = "WASIncompatibleFlavor"
	wasEventReasonInternalError      = "WASInternalError"
)

// classifyWASError maps an assignment error returned by the WAS planner to its
// kueue_was_unsupported_workloads reason label and Warning event reason.
// Normal NoFit outcomes have a nil error and are never classified as unsupported.
func classifyWASError(err error) (metrics.WASUnsupportedReason, string) {
	if err == nil {
		return "", ""
	}
	var multiErr *flavorassigner.MultipleTASFlavorsAssignedError
	if errors.Is(err, flavorassigner.ErrNoTASFlavorAssigned) ||
		errors.As(err, &multiErr) {
		return metrics.WASUnsupportedReasonIncompatibleFlavor, wasEventReasonIncompatibleFlavor
	}
	return metrics.WASUnsupportedReasonInternalError, wasEventReasonInternalError
}
