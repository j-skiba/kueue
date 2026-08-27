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

package core

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDynamicQuotaOrchestratorReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	timeNow := time.Now()
	timeEarlier := timeNow.Add(-10 * time.Minute)

	cases := map[string]struct {
		dqo               *kueuealpha.DynamicQuotaOrchestrator
		capacityProviders []*kueuealpha.CapacityProvider
		cohorts           []*kueue.Cohort
		clusterQueues     []*kueue.ClusterQueue
		otherDQOs         []*kueuealpha.DynamicQuotaOrchestrator
		wantDQO           *kueuealpha.DynamicQuotaOrchestrator
		wantCohorts       []*kueue.Cohort
		wantClusterQueues []*kueue.ClusterQueue
		wantErr           bool
	}{
		"discovery-only: provider not found": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("non-existent-provider", nil).
				Obj(),
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("non-existent-provider", nil).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "CapacityProvider \"non-existent-provider\" not found",
				}).
				Obj(),
			wantErr: false,
		},
		"discovery-only: provider not ready (no condition)": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
					Message: "CapacityProvider \"cp-1\" is not synchronized",
				}).
				Obj(),
			wantErr: false,
		},
		"discovery-only: single provider aggregated successfully": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100"),
							corev1.ResourceMemory: resource.MustParse("50Gi"),
						}).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100"),
					corev1.ResourceMemory: resource.MustParse("50Gi"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: multiple providers with multipliers": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", new(resource.MustParse("0.5"))).
				DiscoveryProvider("cp-2", new(resource.MustParse("0.5"))).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
				utiltestingalpha.MakeCapacityProvider("cp-2").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200"),
						}).
						Flavor("gpu-flavor", corev1.ResourceList{
							"nvidia.com/gpu": resource.MustParse("8"),
						}).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", new(resource.MustParse("0.5"))).
				DiscoveryProvider("cp-2", new(resource.MustParse("0.5"))).
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("150"),
				}).
				EffectiveFlavor("gpu-flavor", corev1.ResourceList{
					"nvidia.com/gpu": resource.MustParse("4"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"distribution to single ClusterQueue": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200"),
						}).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50", "20").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("200"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-cq").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "200", "20").Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		"distribution across Cohort hierarchy with largest remainder method": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cohort").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						}).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-c").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cohort").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("10"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("root-cohort").
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-cohort").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "4").Obj(),
							).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("child-cohort").
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-cohort").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "3").Obj(),
							).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-c").
					Cohort("child-cohort").
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-cohort").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "3").Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		"soft validation: child DQO deactivated when ancestor DQO is distributing": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-child").
				CreationTimestamp(timeNow).
				UID("uid-child").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ancestor").
					CreationTimestamp(timeEarlier).
					UID("uid-ancestor").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-child").
				CreationTimestamp(timeNow).
				UID("uid-child").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with ancestor DynamicQuotaOrchestrator \"dqo-ancestor\"",
				}).
				Obj(),
		},
		"soft validation: duplicate root DQO deactivated by creation timestamp tie-break": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-newer").
				CreationTimestamp(timeNow).
				UID("uid-newer").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-older").
					CreationTimestamp(timeEarlier).
					UID("uid-older").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-newer").
				CreationTimestamp(timeNow).
				UID("uid-newer").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with older DynamicQuotaOrchestrator \"dqo-older\"",
				}).
				Obj(),
		},
		"effective quotas conflict when managed by a different controller": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("other-dqo").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("other-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-other").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonEffectiveQuotasConflict,
					Message: "ClusterQueue \"cq-1\" already managed by DynamicQuotaOrchestrator/other-dqo",
				}).
				Obj(),
		},
		"distribution: previously managed CQ removed from cohort has effective quotas cleared": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-removed").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-1").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100"),
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("dqo-1").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-removed").Obj(),
			},
			wantErr: false,
		},
		"soft validation: child DQO deactivates and clears previously managed effective quotas when ancestor DQO distributes": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavor("default-flavor", corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						}).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("parent-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("parent-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotas(
						utiltestingapi.MakeEffectiveQuotas("child-dqo").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("parent-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "parent-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				EffectiveFlavor("default-flavor", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100"),
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with ancestor DynamicQuotaOrchestrator \"parent-dqo\"",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").Obj(),
			},
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()

			objs := []client.Object{tc.dqo}
			for _, cp := range tc.capacityProviders {
				objs = append(objs, cp)
			}
			for _, co := range tc.cohorts {
				objs = append(objs, co)
			}
			for _, cq := range tc.clusterQueues {
				objs = append(objs, cq)
			}
			for _, other := range tc.otherDQOs {
				objs = append(objs, other)
			}

			cl := builder.WithObjects(objs...).WithStatusSubresource(objs...).Build()
			r := NewDynamicQuotaOrchestratorReconciler(cl)

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: tc.dqo.Name},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Reconcile error = %v, wantErr %v", err, tc.wantErr)
			}

			var gotDQO kueuealpha.DynamicQuotaOrchestrator
			if err := cl.Get(ctx, types.NamespacedName{Name: tc.dqo.Name}, &gotDQO); err != nil {
				t.Fatalf("Failed to get DQO: %v", err)
			}

			if diff := cmp.Diff(tc.wantDQO, &gotDQO,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "CreationTimestamp", "Finalizers"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("Unexpected DQO (-want +got):\n%s", diff)
			}

			for _, wantCQ := range tc.wantClusterQueues {
				var gotCQ kueue.ClusterQueue
				if err := cl.Get(ctx, types.NamespacedName{Name: wantCQ.Name}, &gotCQ); err != nil {
					t.Errorf("Failed to get ClusterQueue %s: %v", wantCQ.Name, err)
					continue
				}
				if diff := cmp.Diff(wantCQ.Status.EffectiveQuotas, gotCQ.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected EffectiveQuotas for ClusterQueue %s (-want +got):\n%s", wantCQ.Name, diff)
				}
			}

			for _, wantCohort := range tc.wantCohorts {
				var gotCohort kueue.Cohort
				if err := cl.Get(ctx, types.NamespacedName{Name: wantCohort.Name}, &gotCohort); err != nil {
					t.Errorf("Failed to get Cohort %s: %v", wantCohort.Name, err)
					continue
				}
				if diff := cmp.Diff(wantCohort.Status.EffectiveQuotas, gotCohort.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected EffectiveQuotas for Cohort %s (-want +got):\n%s", wantCohort.Name, diff)
				}
			}
		})
	}
}
