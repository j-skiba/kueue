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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingv1beta2 "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestFindReplacementAssignment(t *testing.T) {
	// 3-level topology: Block -> Rack -> Hostname
	levels := []string{
		utiltesting.DefaultBlockTopologyLevel,
		utiltesting.DefaultRackTopologyLevel,
		corev1.LabelHostname,
	}

	capacity := corev1.ResourceList{
		corev1.ResourceCPU:  resource.MustParse("1"),
		corev1.ResourcePods: resource.MustParse("32"),
	}

	nodes := []corev1.Node{
		// Block 1, Rack 1
		*node.MakeNode("b1-r1-n1").Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r1").Label(corev1.LabelHostname, "b1-r1-n1").StatusAllocatable(capacity).Obj(),
		*node.MakeNode("b1-r1-n2").Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r1").Label(corev1.LabelHostname, "b1-r1-n2").StatusAllocatable(capacity).Obj(),
		// Block 1, Rack 2
		*node.MakeNode("b1-r2-n1").Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r2").Label(corev1.LabelHostname, "b1-r2-n1").StatusAllocatable(capacity).Obj(),
		*node.MakeNode("b1-r2-n2").Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r2").Label(corev1.LabelHostname, "b1-r2-n2").StatusAllocatable(capacity).Obj(),

		// Block 2, Rack 1
		*node.MakeNode("b2-r1-n1").Label(utiltesting.DefaultBlockTopologyLevel, "b2").Label(utiltesting.DefaultRackTopologyLevel, "r1").Label(corev1.LabelHostname, "b2-r1-n1").StatusAllocatable(capacity).Obj(),
		*node.MakeNode("b2-r1-n2").Label(utiltesting.DefaultBlockTopologyLevel, "b2").Label(utiltesting.DefaultRackTopologyLevel, "r1").Label(corev1.LabelHostname, "b2-r1-n2").StatusAllocatable(capacity).Obj(),
	}

	workload := testingv1beta2.MakeWorkload("wl", "default").
		PodSets(*testingv1beta2.MakePodSet("ps1", 4).
			Request(corev1.ResourceCPU, "1").
			SliceRequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
			SliceSizeTopologyRequest(4).
			Obj()).
		Obj()

	// Initial assignment: All 4 pods in Block 1
	existingAssignment := &utiltas.TopologyAssignment{
		Levels: []string{corev1.LabelHostname},
		Domains: []utiltas.TopologyDomainAssignment{
			{Values: []string{"b1-r1-n1"}, Count: 1},
			{Values: []string{"b1-r1-n2"}, Count: 1},
			{Values: []string{"b1-r2-n1"}, Count: 1},
			{Values: []string{"b1-r2-n2"}, Count: 1},
		},
	}

	// b1-r1-n1 fails/tainted

	// Add a spare node in Block 1.
	nodes = append(nodes, *node.MakeNode("b1-r2-n3").Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r2").Label(corev1.LabelHostname, "b1-r2-n3").StatusAllocatable(capacity).Obj())

	workload.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: "b1-r1-n1"}}

	// Setup snapshot
	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "default", levels, nil)
	for _, n := range nodes {
		s.addNode(n)
	}
	s.initialize()

	// Busy nodes
	busyNodes := []string{"b1-r1-n2", "b1-r2-n1", "b1-r2-n2"}
	for _, name := range busyNodes {
		s.leaves[utiltas.TopologyDomainID(name)].freeCapacity[corev1.ResourceCPU] = 0
	}

	s.leaves["b1-r1-n1"].node.Spec.Taints = []corev1.Taint{
		{Key: "instance", Value: "unhealthy", Effect: corev1.TaintEffectNoSchedule},
	}

	req := TASPodSetRequests{
		PodSet:            &workload.Spec.PodSets[0],
		SinglePodRequests: resources.Requests{corev1.ResourceCPU: 1},
		Count:             4,
	}

	newAssignment, _, errReason := s.findReplacementAssignment(&req, existingAssignment, workload, nil)
	if errReason != "" {
		t.Fatalf("Failed to find replacement: %s", errReason)
	}

	foundNewNode := false
	for _, d := range newAssignment.Domains {
		nodeName := d.Values[len(d.Values)-1]
		if nodeName[0:2] != "b1" {
			t.Errorf("Found assignment in wrong block: %v", d.Values)
		}
		if nodeName == "b1-r2-n3" {
			foundNewNode = true
		}
	}
	if !foundNewNode {
		t.Errorf("Did not find expected replacement node b1-r2-n3. Got: %v", newAssignment.Domains)
	}
}
