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
	"cmp"
	"errors"
	"fmt"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
)

func assertWASUnsupportedMetricSeries(t *testing.T, want ...testingmetrics.MetricDataPoint) {
	t.Helper()
	got := testingmetrics.CollectFilteredGaugeVec(metrics.WASUnsupportedWorkloads, nil)
	sortPoints := cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool {
		for _, k := range []string{"cluster_queue", "reason", "replica_role"} {
			if c := cmp.Compare(a.Labels[k], b.Labels[k]); c != 0 {
				return c < 0
			}
		}
		return false
	})
	if diff := gocmp.Diff(want, got, cmpopts.EquateEmpty(), sortPoints); diff != "" {
		t.Errorf("Unexpected kueue_was_unsupported_workloads series (-want,+got):\n%s", diff)
	}
}

func wasMetricPoint(cq kueue.ClusterQueueReference, reason metrics.WASUnsupportedReason, val float64) testingmetrics.MetricDataPoint {
	return testingmetrics.MetricDataPoint{
		Labels: map[string]string{
			"cluster_queue": string(cq),
			"reason":        string(reason),
			"replica_role":  roletracker.RoleStandalone,
		},
		Value: val,
	}
}

func TestClassifyWASError(t *testing.T) {
	cases := map[string]struct {
		err        error
		wantReason metrics.WASUnsupportedReason
		wantEvent  string
	}{
		"nil error": {},
		"no TAS flavor": {
			err:        fmt.Errorf("wrapped: %w", flavorassigner.ErrNoTASFlavorAssigned),
			wantReason: metrics.WASUnsupportedReasonIncompatibleFlavor,
			wantEvent:  wasEventReasonIncompatibleFlavor,
		},
		"missing hostname level": {
			err:        fmt.Errorf("wrapped: %w", flavorassigner.ErrTASFlavorMissingHostnameLevel),
			wantReason: metrics.WASUnsupportedReasonIncompatibleFlavor,
			wantEvent:  wasEventReasonIncompatibleFlavor,
		},
		"multiple TAS flavors": {
			err:        fmt.Errorf("wrapped: %w", &flavorassigner.MultipleTASFlavorsAssignedError{}),
			wantReason: metrics.WASUnsupportedReasonIncompatibleFlavor,
			wantEvent:  wasEventReasonIncompatibleFlavor,
		},
		"internal error": {
			err:        errors.New("simulator failure"),
			wantReason: metrics.WASUnsupportedReasonInternalError,
			wantEvent:  wasEventReasonInternalError,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotReason, gotEvent := classifyWASError(tc.err)
			if gotReason != tc.wantReason || gotEvent != tc.wantEvent {
				t.Errorf("classifyWASError(%v) = (%q, %q), want (%q, %q)", tc.err, gotReason, gotEvent, tc.wantReason, tc.wantEvent)
			}
		})
	}
}
