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
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// testInadmissibleWorkloadRequeuer buffers requeue events
// to be processed synchronously, for use in unit tests.
type testInadmissibleWorkloadRequeuer struct {
	manager *Manager
	cqs     map[kueue.ClusterQueueReference]EventType
	cohorts map[kueue.CohortReference]EventType
}

func (r *testInadmissibleWorkloadRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference, eventType EventType) {
	r.cqs[cqName] = eventType
}

func (r *testInadmissibleWorkloadRequeuer) notifyCohort(cohortName kueue.CohortReference, eventType EventType) {
	r.cohorts[cohortName] = eventType
}

func (r *testInadmissibleWorkloadRequeuer) setManager(manager *Manager) {
	r.manager = manager
}

// ProcessRequeues requeues all the inadmissible workloads
// belonging to Cohorts/Queues which were notified.
// Returns the total number of workloads moved.
func (w *testInadmissibleWorkloadRequeuer) ProcessRequeues(ctx context.Context) int {
	total := 0
	for cqName, eventType := range w.cqs {
		total += requeueWorkloadsCQ(ctx, w.manager, cqName, eventType)
	}
	for cohortName, eventType := range w.cohorts {
		total += requeueWorkloadsCohort(ctx, w.manager, cohortName, eventType)
	}
	w.cqs = make(map[kueue.ClusterQueueReference]EventType)
	w.cohorts = make(map[kueue.CohortReference]EventType)
	return total
}

// NewManagerForUnitTests creates a new Manager for testing purposes.
// This test manager, though exported, is not included in Kueue binary.
// Note that this function is not found when running:
// make build && go tool nm ./bin/manager | grep "NewManager"
func NewManagerForUnitTests(client client.Client, checker StatusChecker, options ...Option) *Manager {
	manager, _ := NewManagerForUnitTestsWithRequeuer(client, checker, options...)
	return manager
}

// NewManagerForUnitTestsWithRequeuer creates a new Manager for testing purposes, pre-configured with a testInadmissibleWorkloadRequeuer.
func NewManagerForUnitTestsWithRequeuer(client client.Client, checker StatusChecker, options ...Option) (*Manager, *testInadmissibleWorkloadRequeuer) {
	requeuer := &testInadmissibleWorkloadRequeuer{
		cqs:     make(map[kueue.ClusterQueueReference]EventType),
		cohorts: make(map[kueue.CohortReference]EventType),
	}

	manager := NewManager(client, checker, requeuer, options...)
	return manager, requeuer
}
