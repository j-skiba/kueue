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
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

func podsForPodSet(ctx context.Context, c client.Client, ns, wlName string, psName kueue.PodSetReference) ([]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
		constants.PodSetLabel: string(psName),
	}, client.MatchingFields{
		indexer.WorkloadNameKey: wlName,
	}); err != nil {
		return nil, err
	}
	result := make([]*corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		if utilpod.IsTerminated(&pods.Items[i]) {
			continue
		}
		result = append(result, &pods.Items[i])
	}
	return result, nil
}

func calculateRankOffsets(wl *kueue.Workload) (map[kueue.PodSetReference]int32, map[kueue.PodSetReference]int32) {
	groupedPodSetAssignments := make(map[string][]*kueue.PodSetAssignment)
	psNameToTopologyRequest := make(map[kueue.PodSetReference]*kueue.PodSetTopologyRequest)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.TopologyRequest != nil {
			psNameToTopologyRequest[ps.Name] = ps.TopologyRequest
		}
	}

	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		groupName := strconv.Itoa(i)
		if psNameToTopologyRequest[psa.Name] != nil && psNameToTopologyRequest[psa.Name].PodSetGroupName != nil {
			groupName = *psNameToTopologyRequest[psa.Name].PodSetGroupName
		}
		groupedPodSetAssignments[groupName] = append(groupedPodSetAssignments[groupName], psa)
	}

	rankOffsets := make(map[kueue.PodSetReference]int32)
	maxRank := make(map[kueue.PodSetReference]int32)

	for _, psas := range groupedPodSetAssignments {
		if len(psas) > 1 {
			smallerPsa := psas[0]
			largerPsa := psas[1]
			if *smallerPsa.Count > *largerPsa.Count {
				smallerPsa = psas[1]
				largerPsa = psas[0]
			}
			rankOffsets[smallerPsa.Name] = 0
			rankOffsets[largerPsa.Name] = *smallerPsa.Count
			maxRank[smallerPsa.Name] = *smallerPsa.Count
			maxRank[largerPsa.Name] = *largerPsa.Count + *smallerPsa.Count
		} else {
			rankOffsets[psas[0].Name] = 0
			maxRank[psas[0].Name] = *psas[0].Count
		}
	}
	return rankOffsets, maxRank
}
