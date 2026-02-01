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

package tas

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestNodeFailureReconciler_Reproduction(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)
	nodeName := "kind-worker"
	wlName := "jobset-tas-jobset-block2-73d73"
	nsName := "default"
	fakeClock := testingclock.NewFakeClock(testStartTime)
	wlKey := types.NamespacedName{Name: wlName, Namespace: nsName}

	baseWorkload := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(func() kueue.PodSet {
			ps := *utiltestingapi.MakePodSet("worker", 2).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ps.Template.ObjectMeta.Annotations = map[string]string{"kueue.x-k8s.io/podset-required-topology": "block"}
			return ps
		}()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("tas-cq").
				PodSets(utiltestingapi.MakePodSetAssignment("worker").
					Assignment(corev1.ResourceCPU, "tas-flavor", "1").
					Count(2).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()). // Counts on Node
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	now := metav1.NewTime(fakeClock.Now())

	failedPod := testingpod.MakePod("tas-jobset-block2-worker-0-0-h9fbs", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation("kueue.x-k8s.io/podset-required-topology", "block"). // Verify IsTAS passes
		NodeName(nodeName).
		Obj()
	failedPod.Status.Phase = corev1.PodFailed
	failedPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{FinishedAt: now, ExitCode: 1},
			},
		},
	}

	taintedNode := testingnode.MakeNode(nodeName).
		StatusConditions(corev1.NodeCondition{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
		}).
		Taints(corev1.Taint{Key: "nvidia.com", Value: "true", Effect: corev1.TaintEffectNoSchedule}).
		Obj()

	tests := map[string]struct {
		initObjs           []client.Object
		reconcileRequests  []reconcile.Request
		wantUnhealthyNodes []kueue.UnhealthyNode
		featureGates       map[featuregate.Feature]bool
	}{
		"Reproduction: Node Ready+Tainted, Pod Failed -> Should be Unhealthy": {
			featureGates: map[featuregate.Feature]bool{
				features.TASFailedNodeReplacement:       true,
				features.TASReplaceNodeOnPodTermination: true,
				features.TopologyAwareScheduling:        true,
			},
			initObjs: []client.Object{
				taintedNode,
				baseWorkload.DeepCopy(),
				failedPod,
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			for fg, enable := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enable)
			}
			fakeClock.SetTime(testStartTime)

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(tc.initObjs...).
				WithStatusSubresource(tc.initObjs...).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			ctx, _ := utiltesting.ContextWithLog(t)
			err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			if err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			r := newNodeFailureReconciler(cl, recorder, nil) // TASFailedNodeReplacement enables this controller
			r.clock = fakeClock

			var errReconcile error
			for _, req := range tc.reconcileRequests {
				_, errReconcile = r.Reconcile(ctx, req)
				if errReconcile != nil {
					t.Errorf("Reconcile() error = %v for request %v", errReconcile, req)
				}
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}

			if diff := cmp.Diff(tc.wantUnhealthyNodes, wl.Status.UnhealthyNodes, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected unhealthyNodes in status (-want/+got):\n%s", diff)
			}
		})
	}
}
