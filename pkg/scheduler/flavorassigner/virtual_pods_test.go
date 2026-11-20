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

package flavorassigner

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCandidateVirtualPods(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newBookmarkSnapshot(ctx, t, log, "10", "0", kueue.FlavorFungibility{})

	wl := utiltestingapi.MakeWorkload("wl", "default").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
			Request(corev1.ResourceCPU, "1").
			Labels(map[string]string{"app": "worker"}).
			Annotations(map[string]string{"meta": "data"}).
			NodeSelector(map[string]string{"arch": "amd64"}).
			Toleration(corev1.Toleration{Key: "arch", Value: "amd64", Effect: corev1.TaintEffectNoSchedule}).
			Obj()).
		AdmissionChecks(
			kueue.AdmissionCheckState{
				Name:  "check-ready-1",
				State: kueue.CheckStateReady,
				PodSetUpdates: []kueue.PodSetUpdate{
					{
						Name:         kueue.DefaultPodSetName,
						NodeSelector: map[string]string{"zone": "zone-a"},
						Labels:       map[string]string{"injected-1": "true"},
						Annotations:  map[string]string{"injected-ann-1": "val-1"},
						Tolerations: []corev1.Toleration{
							{Key: "zone", Value: "zone-a", Effect: corev1.TaintEffectNoSchedule},
						},
					},
				},
			},
			kueue.AdmissionCheckState{
				Name:  "check-ready-2",
				State: kueue.CheckStateReady,
				PodSetUpdates: []kueue.PodSetUpdate{
					{
						Name:         kueue.DefaultPodSetName,
						NodeSelector: map[string]string{"region": "us-central1"},
						Labels:       map[string]string{"injected-2": "true"},
						Annotations:  map[string]string{"injected-ann-2": "val-2"},
					},
				},
			},
			kueue.AdmissionCheckState{
				Name:  "check-pending",
				State: kueue.CheckStatePending,
				PodSetUpdates: []kueue.PodSetUpdate{
					{
						Name:         kueue.DefaultPodSetName,
						NodeSelector: map[string]string{"ignored-key": "ignored-val"},
						Labels:       map[string]string{"ignored-label": "true"},
					},
				},
			},
		).
		Obj()
	wlInfo := workload.NewInfo(log, wl)

	conflictWl := utiltestingapi.MakeWorkload("wl-conflict", "default").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "1").
			NodeSelector(map[string]string{"arch": "amd64"}).
			Obj()).
		AdmissionChecks(
			kueue.AdmissionCheckState{
				Name:  "check-conflict",
				State: kueue.CheckStateReady,
				PodSetUpdates: []kueue.PodSetUpdate{
					{
						Name:         kueue.DefaultPodSetName,
						NodeSelector: map[string]string{"arch": "arm64"},
					},
				},
			},
		).
		Obj()
	conflictInfo := workload.NewInfo(log, conflictWl)

	cases := map[string]struct {
		workload           *workload.Info
		assignment         Assignment
		wantPodsCount      int
		wantNodeSelector   map[string]string
		wantLabels         map[string]string
		wantAnnotations    map[string]string
		wantTolerationsLen int
		wantErr            bool
	}{
		"creates candidate pods for assigned count with ready updates and flavor labels": {
			workload: wlInfo,
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:  kueue.DefaultPodSetName,
						Count: 2,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit},
						},
						Status: *NewStatus(),
					},
				},
			},
			wantPodsCount: 2,
			wantNodeSelector: map[string]string{
				"arch":   "amd64",
				"zone":   "zone-a",
				"region": "us-central1",
				"flavor": "one",
			},
			wantLabels: map[string]string{
				"app":                 "worker",
				"injected-1":          "true",
				"injected-2":          "true",
				constants.PodSetLabel: string(kueue.DefaultPodSetName),
			},
			wantAnnotations: map[string]string{
				"meta":                   "data",
				"injected-ann-1":         "val-1",
				"injected-ann-2":         "val-2",
				kueue.WorkloadAnnotation: "wl",
			},
			wantTolerationsLen: 2,
		},
		"returns conflict error when PodSetUpdate conflicts with PodSet nodeSelector": {
			workload: conflictInfo,
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit},
						},
						Status: *NewStatus(),
					},
				},
			},
			wantErr: true,
		},
		"returns error for non-TAS podset": {
			workload: wlInfo,
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "non-tas-flavor", Mode: Fit},
						},
						Status: *NewStatus(),
					},
				},
			},
			wantErr: true,
		},
		"skips zero-count podset on non-TAS flavor": {
			workload: wlInfo,
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:  kueue.DefaultPodSetName,
						Count: 0,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "non-tas-flavor", Mode: Fit},
						},
						Status: *NewStatus(),
					},
				},
			},
			wantPodsCount: 0,
		},
		"returns error when podset is failing": {
			workload: wlInfo,
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:   kueue.DefaultPodSetName,
						Count:  1,
						Status: Status{err: errors.New("podset failure")},
					},
				},
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pods, err := tc.assignment.CandidateVirtualPods(tc.workload, cq, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CandidateVirtualPods() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			if len(pods) != tc.wantPodsCount {
				t.Fatalf("expected %d candidate pods, got %d", tc.wantPodsCount, len(pods))
			}

			for i, pod := range pods {
				if diff := cmp.Diff(tc.wantNodeSelector, pod.Spec.NodeSelector); diff != "" {
					t.Errorf("pod[%d] nodeSelector mismatch (-want +got):\n%s", i, diff)
				}
				for k, v := range tc.wantLabels {
					if pod.Labels[k] != v {
						t.Errorf("pod[%d] label %s = %q, want %q", i, k, pod.Labels[k], v)
					}
				}
				if pod.Labels["ignored-label"] != "" {
					t.Errorf("pod[%d] should not contain labels from non-ready admission check", i)
				}
				for k, v := range tc.wantAnnotations {
					if pod.Annotations[k] != v {
						t.Errorf("pod[%d] annotation %s = %q, want %q", i, k, pod.Annotations[k], v)
					}
				}
				if len(pod.Spec.Tolerations) != tc.wantTolerationsLen {
					t.Errorf("pod[%d] expected %d tolerations, got %d", i, tc.wantTolerationsLen, len(pod.Spec.Tolerations))
				}
			}
		})
	}
}

func TestCandidateVirtualPods_MissingHostnameLevel(t *testing.T) {
	const blockLabel = "cloud.provider.com/topology-block"

	cases := map[string]struct {
		nodeFeasibility bool
		levels          []string
		count           int32
		wantErr         error
	}{
		"topology omits hostname with TASNodeFeasibilityForAllLevels disabled": {
			nodeFeasibility: false,
			levels:          []string{blockLabel},
			count:           1,
			wantErr:         ErrTASFlavorMissingHostnameLevel,
		},
		"topology omits hostname with TASNodeFeasibilityForAllLevels enabled": {
			nodeFeasibility: true,
			levels:          []string{blockLabel},
			count:           1,
			wantErr:         ErrTASFlavorMissingHostnameLevel,
		},
		"skips zero-count podset when topology omits hostname": {
			nodeFeasibility: true,
			levels:          []string{blockLabel},
			count:           0,
		},
		"topology declares hostname with TASNodeFeasibilityForAllLevels disabled": {
			nodeFeasibility: false,
			levels:          []string{blockLabel, corev1.LabelHostname},
			count:           1,
		},
		"topology declares hostname with TASNodeFeasibilityForAllLevels enabled": {
			nodeFeasibility: true,
			levels:          []string{blockLabel, corev1.LabelHostname},
			count:           1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, tc.nodeFeasibility)
			ctx, log := utiltesting.ContextWithLog(t)

			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("tas", "tas-flavor").
				TopologyName("tas-topology").
				Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("tas-topology").Levels(tc.levels...).Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode("node-1").
				Label("tas", "tas-flavor").
				Label(blockLabel, "block-1").
				Label(corev1.LabelHostname, "node-1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("4"),
					corev1.ResourcePods: resource.MustParse("32"),
				}).
				Ready().
				Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("building snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue("cq")

			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "default").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, int(tc.count)).
					RequiredTopologyRequest(blockLabel).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj())
			assignment := Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:  kueue.DefaultPodSetName,
						Count: tc.count,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "tas-flavor", Mode: Fit},
						},
						Status: *NewStatus(),
					},
				},
			}

			pods, err := assignment.CandidateVirtualPods(wl, cq, nil)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("CandidateVirtualPods() error mismatch (-want +got):\n%s", diff)
			}
			if tc.wantErr != nil {
				if pods != nil {
					t.Errorf("CandidateVirtualPods() returned %d pods on error, want nil", len(pods))
				}
				return
			}
			if len(pods) != int(tc.count) {
				t.Errorf("CandidateVirtualPods() returned %d pods, want %d", len(pods), tc.count)
			}
		})
	}
}
