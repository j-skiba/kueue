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
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	dqoControllerName = "dynamicquotaorchestrator-reconciler"
	dqoFinalizerName  = "kueue.x-k8s.io/dynamic-quota-orchestrator"
)

type DynamicQuotaOrchestratorReconciler struct {
	client      client.Client
	logName     string
	roleTracker *roletracker.RoleTracker
}

type DQOReconcilerOption func(*DynamicQuotaOrchestratorReconciler)

// DQOWithRoleTracker configures the RoleTracker for the reconciler.
func DQOWithRoleTracker(rt *roletracker.RoleTracker) DQOReconcilerOption {
	return func(r *DynamicQuotaOrchestratorReconciler) {
		r.roleTracker = rt
	}
}

// NewDynamicQuotaOrchestratorReconciler instantiates a new DynamicQuotaOrchestrator reconciler.
func NewDynamicQuotaOrchestratorReconciler(client client.Client, opts ...DQOReconcilerOption) *DynamicQuotaOrchestratorReconciler {
	r := &DynamicQuotaOrchestratorReconciler{
		client:  client,
		logName: dqoControllerName,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *DynamicQuotaOrchestratorReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts/status,verbs=get;update;patch

// SetupWithManager registers the DynamicQuotaOrchestrator controller and its watches with the manager.
func (r *DynamicQuotaOrchestratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.DynamicQuotaOrchestrator{}).
		Watches(
			&kueuealpha.CapacityProvider{},
			handler.EnqueueRequestsFromMapFunc(r.mapCapacityProviderToDQOs),
		).
		Watches(
			&kueue.Cohort{},
			handler.EnqueueRequestsFromMapFunc(r.mapDistributingDQOs),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&kueue.ClusterQueue{},
			handler.EnqueueRequestsFromMapFunc(r.mapDistributingDQOs),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&kueuealpha.DynamicQuotaOrchestrator{},
			handler.EnqueueRequestsFromMapFunc(r.mapOtherDistributingDQOs),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// mapCapacityProviderToDQOs maps a CapacityProvider event to reconcile requests for all DynamicQuotaOrchestrators referencing it.
func (r *DynamicQuotaOrchestratorReconciler) mapCapacityProviderToDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
	capacityProvider, ok := obj.(*kueuealpha.CapacityProvider)
	if !ok {
		return nil
	}
	var orchestratorList kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &orchestratorList, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorCapacityProviderKey: capacityProvider.Name,
	}); err != nil {
		r.logger().Error(err, "Failed to list DynamicQuotaOrchestrators for CapacityProvider", "capacityProvider", capacityProvider.Name)
		return nil
	}
	requests := make([]ctrl.Request, 0, len(orchestratorList.Items))
	for _, orchestrator := range orchestratorList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: orchestrator.Name},
		})
	}
	return requests
}

// mapDistributingDQOs maps Cohort or ClusterQueue events to reconcile requests for all distributing DynamicQuotaOrchestrators.
func (r *DynamicQuotaOrchestratorReconciler) mapDistributingDQOs(ctx context.Context, _ client.Object) []ctrl.Request {
	var orchestratorList kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &orchestratorList, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorIsDistributingKey: "true",
	}); err != nil {
		r.logger().Error(err, "Failed to list distributing DynamicQuotaOrchestrators")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(orchestratorList.Items))
	for _, orchestrator := range orchestratorList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: orchestrator.Name},
		})
	}
	return requests
}

// mapOtherDistributingDQOs maps a DynamicQuotaOrchestrator event to reconcile requests for other distributing DynamicQuotaOrchestrators.
func (r *DynamicQuotaOrchestratorReconciler) mapOtherDistributingDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
	orchestrator, ok := obj.(*kueuealpha.DynamicQuotaOrchestrator)
	if !ok {
		return nil
	}
	if orchestrator.Spec.CapacityDistribution == nil {
		return nil
	}
	return r.mapDistributingDQOs(ctx, obj)
}

// Reconcile coordinates capacity discovery and quota distribution for a DynamicQuotaOrchestrator.
func (r *DynamicQuotaOrchestratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DynamicQuotaOrchestration) {
		return ctrl.Result{}, nil
	}

	log := r.logger().WithValues("dynamicQuotaOrchestrator", req.NamespacedName)
	log.V(4).Info("Reconcile DynamicQuotaOrchestrator")

	var orchestrator kueuealpha.DynamicQuotaOrchestrator
	if err := r.client.Get(ctx, req.NamespacedName, &orchestrator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !orchestrator.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&orchestrator, dqoFinalizerName) {
			if err := r.clearManagedEffectiveQuotas(ctx, orchestrator.Name); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&orchestrator, dqoFinalizerName)
			if err := r.client.Update(ctx, &orchestrator); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&orchestrator, dqoFinalizerName) {
		if err := r.client.Update(ctx, &orchestrator); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	oldStatus := orchestrator.Status.DeepCopy()

	// Phase 1: Capacity Discovery
	effectiveCapacity, discoveryErr := r.reconcileDiscovery(ctx, &orchestrator)
	if discoveryErr != nil {
		_ = r.updateStatus(ctx, &orchestrator, oldStatus)
		return ctrl.Result{}, discoveryErr
	}

	// Phase 2: Quota Distribution
	var distributionErr error
	switch {
	case orchestrator.Spec.CapacityDistribution == nil:
		apimeta.RemoveStatusCondition(&orchestrator.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
		if err := r.clearManagedEffectiveQuotas(ctx, orchestrator.Name); err != nil {
			log.Error(err, "Failed to clear managed effective quotas for discovery-only DQO")
			distributionErr = err
		}
	case effectiveCapacity == nil:
		apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: orchestrator.Generation,
			Reason:             kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed,
			Message:            "Capacity discovery not ready",
		})
	default:
		if err := r.reconcileDistribution(ctx, &orchestrator, effectiveCapacity); err != nil {
			log.Error(err, "Failed to distribute quotas")
			distributionErr = err
		}
	}

	if err := r.updateStatus(ctx, &orchestrator, oldStatus); err != nil {
		return ctrl.Result{}, err
	}
	if distributionErr != nil {
		return ctrl.Result{}, distributionErr
	}
	return ctrl.Result{}, nil
}

// clearManagedEffectiveQuotas removes status.effectiveQuotas on all ClusterQueues and Cohorts previously managed by this orchestrator.
func (r *DynamicQuotaOrchestratorReconciler) clearManagedEffectiveQuotas(ctx context.Context, orchestratorName string) error {
	var clusterQueueList kueue.ClusterQueueList
	if err := r.client.List(ctx, &clusterQueueList, client.MatchingFields{
		indexer.ClusterQueueEffectiveQuotaOrchestratorKey: orchestratorName,
	}); err != nil {
		return err
	}
	for i := range clusterQueueList.Items {
		clusterQueue := &clusterQueueList.Items[i]
		clusterQueue.Status.EffectiveQuotas = nil
		if err := r.client.Status().Update(ctx, clusterQueue); err != nil {
			return err
		}
	}

	var cohortList kueue.CohortList
	if err := r.client.List(ctx, &cohortList, client.MatchingFields{
		indexer.CohortEffectiveQuotaOrchestratorKey: orchestratorName,
	}); err != nil {
		return err
	}
	for i := range cohortList.Items {
		cohort := &cohortList.Items[i]
		cohort.Status.EffectiveQuotas = nil
		if err := r.client.Status().Update(ctx, cohort); err != nil {
			return err
		}
	}
	return nil
}

// reconcileDiscovery performs Phase 1 reconciliation: aggregates normalized capacities across referenced CapacityProviders.
func (r *DynamicQuotaOrchestratorReconciler) reconcileDiscovery(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator) (*kueuealpha.EffectiveCapacity, error) {
	aggregatedCapacity := make(map[kueuealpha.ResourceFlavorReference]corev1.ResourceList)

	for _, providerContribution := range orchestrator.Spec.CapacityDiscovery.Providers {
		var capacityProvider kueuealpha.CapacityProvider
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(providerContribution.Name)}, &capacityProvider); err != nil {
			if apierrors.IsNotFound(err) {
				r.setDiscoveryCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured, fmt.Sprintf("CapacityProvider %q not found", providerContribution.Name))
				orchestrator.Status.EffectiveCapacity = nil
				return nil, nil
			}
			return nil, err
		}

		syncCondition := apimeta.FindStatusCondition(capacityProvider.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
		if syncCondition == nil || syncCondition.Status != metav1.ConditionTrue || capacityProvider.Status.Capacity == nil {
			r.setDiscoveryCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady, fmt.Sprintf("CapacityProvider %q is not synchronized", providerContribution.Name))
			orchestrator.Status.EffectiveCapacity = nil
			return nil, nil
		}

		aggregateProviderCapacity(capacityProvider.Status.Capacity, providerContribution.EffectiveCapacityMultiplier, aggregatedCapacity)
	}

	effectiveCapacityFlavors := make([]kueuealpha.EffectiveCapacityFlavor, 0, len(aggregatedCapacity))
	flavorNames := make([]string, 0, len(aggregatedCapacity))
	for flavorName := range aggregatedCapacity {
		flavorNames = append(flavorNames, string(flavorName))
	}
	slices.Sort(flavorNames)
	for _, flavorName := range flavorNames {
		flavorRef := kueuealpha.ResourceFlavorReference(flavorName)
		effectiveCapacityFlavors = append(effectiveCapacityFlavors, kueuealpha.EffectiveCapacityFlavor{
			Name:      flavorRef,
			Resources: aggregatedCapacity[flavorRef],
		})
	}

	effectiveCapacity := &kueuealpha.EffectiveCapacity{
		Flavors: effectiveCapacityFlavors,
	}
	orchestrator.Status.EffectiveCapacity = effectiveCapacity
	r.setDiscoveryCondition(orchestrator, metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonComputed, "Aggregated capacity successfully computed")
	return effectiveCapacity, nil
}

// aggregateProviderCapacity scales and adds flavor resource quantities from a single CapacityProvider into the running aggregated total.
func aggregateProviderCapacity(
	capacity *kueuealpha.CapacityProviderNormalizedCapacity,
	multiplier *resource.Quantity,
	aggregatedCapacity map[kueuealpha.ResourceFlavorReference]corev1.ResourceList,
) {
	if capacity == nil {
		return
	}
	for _, flavor := range capacity.Flavors {
		if _, exists := aggregatedCapacity[flavor.Name]; !exists {
			aggregatedCapacity[flavor.Name] = make(corev1.ResourceList)
		}
		for resourceName, resourceQuantity := range flavor.Resources {
			scaled := resourceQuantity
			if multiplier != nil {
				scaled = multiplyQuantity(resourceQuantity, *multiplier)
			}
			current := aggregatedCapacity[flavor.Name][resourceName]
			current.Add(scaled)
			aggregatedCapacity[flavor.Name][resourceName] = current
		}
	}
}

// multiplyQuantity multiplies a resource.Quantity by another resource.Quantity using arbitrary precision decimal arithmetic.
func multiplyQuantity(qty, multiplier resource.Quantity) resource.Quantity {
	var product inf.Dec
	product.Mul(qty.AsDec(), multiplier.AsDec())
	return *resource.NewDecimalQuantity(product, qty.Format)
}

// reconcileDistribution performs Phase 2 reconciliation: validates subtree conflicts, resolves the target hierarchy, and distributes effective quotas.
func (r *DynamicQuotaOrchestratorReconciler) reconcileDistribution(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, effectiveCapacity *kueuealpha.EffectiveCapacity) error {
	var otherOrchestrators kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &otherOrchestrators, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorIsDistributingKey: "true",
	}); err != nil {
		return err
	}

	if conflictMsg, conflicted := r.findConflictingDistributingDQO(ctx, orchestrator, otherOrchestrators.Items); conflicted {
		r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator, conflictMsg)
		if err := r.clearManagedEffectiveQuotas(ctx, orchestrator.Name); err != nil {
			return err
		}
		return nil
	}

	targetClusterQueues, targetCohorts, err := r.resolveSubtree(ctx, orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured, fmt.Sprintf("%s %q not found", orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef.Kind, orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef.Name))
			return nil
		}
		if strings.HasPrefix(err.Error(), "unsupported subtree root kind") {
			r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured, err.Error())
			return nil
		}
		return err
	}

	if conflictMsg, conflicted := findOwnershipConflict(orchestrator.Name, targetClusterQueues, targetCohorts, otherOrchestrators.Items); conflicted {
		r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonEffectiveQuotasConflict, conflictMsg)
		if err := r.clearManagedEffectiveQuotas(ctx, orchestrator.Name); err != nil {
			return err
		}
		return nil
	}

	allocatedQuantities := calculateAllocations(effectiveCapacity, targetCohorts, targetClusterQueues)
	if err := r.applyEffectiveQuotas(ctx, orchestrator.Name, targetClusterQueues, targetCohorts, allocatedQuantities); err != nil {
		return err
	}

	r.setDistributionCondition(orchestrator, metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed, "Quotas successfully distributed")
	return nil
}

// findConflictingDistributingDQO checks if another distributing DQO conflicts by being an ancestor or an older instance on the same root.
func (r *DynamicQuotaOrchestratorReconciler) findConflictingDistributingDQO(
	ctx context.Context,
	orchestrator *kueuealpha.DynamicQuotaOrchestrator,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) (string, bool) {
	currentRoot := orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
	for _, otherOrchestrator := range otherOrchestrators {
		if otherOrchestrator.Name == orchestrator.Name || otherOrchestrator.Spec.CapacityDistribution == nil || !otherOrchestrator.DeletionTimestamp.IsZero() {
			continue
		}

		otherRoot := otherOrchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
		if isAncestor, err := r.isStrictAncestor(ctx, otherRoot, currentRoot); err == nil && isAncestor {
			return fmt.Sprintf("Conflicts with ancestor DynamicQuotaOrchestrator %q", otherOrchestrator.Name), true
		}

		if otherRoot == currentRoot && isOtherOlder(&otherOrchestrator, orchestrator) {
			return fmt.Sprintf("Conflicts with older DynamicQuotaOrchestrator %q", otherOrchestrator.Name), true
		}
	}
	return "", false
}

// findOwnershipConflict checks whether any target ClusterQueue or Cohort is currently managed by another active distributing DQO.
func findOwnershipConflict(
	orchestratorName string,
	targetClusterQueues []kueue.ClusterQueue,
	targetCohorts []kueue.Cohort,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) (string, bool) {
	for _, clusterQueue := range targetClusterQueues {
		if conflictMsg, conflicted := checkManagedConflict("ClusterQueue", clusterQueue.Name, clusterQueue.Status.EffectiveQuotas, orchestratorName, otherOrchestrators); conflicted {
			return conflictMsg, true
		}
	}
	for _, cohort := range targetCohorts {
		if conflictMsg, conflicted := checkManagedConflict("Cohort", cohort.Name, cohort.Status.EffectiveQuotas, orchestratorName, otherOrchestrators); conflicted {
			return conflictMsg, true
		}
	}
	return "", false
}

// checkManagedConflict verifies whether an individual object's EffectiveQuotas is owned by another active distributing DQO.
func checkManagedConflict(
	kind, name string,
	quotas *kueue.EffectiveQuotaStatus,
	orchestratorName string,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) (string, bool) {
	if quotas != nil {
		ref := quotas.OrchestratorRef
		if ref.Name != orchestratorName && isOtherActiveDistributingDQO(ref, otherOrchestrators) {
			return fmt.Sprintf("%s %q already managed by %s/%s", kind, name, ref.Kind, ref.Name), true
		}
	}
	return "", false
}

// calculateAllocations distributes total capacity across all flavors and resources to participant nodes in the subtree.
func calculateAllocations(
	effectiveCapacity *kueuealpha.EffectiveCapacity,
	cohorts []kueue.Cohort,
	clusterQueues []kueue.ClusterQueue,
) map[quotaKey]map[string]resource.Quantity {
	allocatedQuantities := make(map[quotaKey]map[string]resource.Quantity)
	for _, flavor := range effectiveCapacity.Flavors {
		for resourceName, totalCapacity := range flavor.Resources {
			key := quotaKey{flavor: flavor.Name, resource: resourceName}
			participants := collectParticipants(flavor.Name, resourceName, cohorts, clusterQueues)
			allocatedQuantities[key] = distributeCapacityProportionally(totalCapacity, participants)
		}
	}
	return allocatedQuantities
}

// applyEffectiveQuotas updates status.effectiveQuotas on all target ClusterQueues and Cohorts in the subtree,
// and clears effectiveQuotas from any queues that were previously managed by this orchestrator but are no longer in the subtree.
func (r *DynamicQuotaOrchestratorReconciler) applyEffectiveQuotas(
	ctx context.Context,
	orchestratorName string,
	targetClusterQueues []kueue.ClusterQueue,
	targetCohorts []kueue.Cohort,
	allocatedQuantities map[quotaKey]map[string]resource.Quantity,
) error {
	targetCQNames := sets.New[string]()
	for i := range targetClusterQueues {
		clusterQueue := &targetClusterQueues[i]
		targetCQNames.Insert(clusterQueue.Name)
		newEffectiveQuotas := buildEffectiveQuotas(orchestratorName, clusterQueue.Spec.ResourceGroups, allocatedQuantities, getNodeID(false, clusterQueue.Name))
		if !equality.Semantic.DeepEqual(clusterQueue.Status.EffectiveQuotas, newEffectiveQuotas) {
			clusterQueue.Status.EffectiveQuotas = newEffectiveQuotas
			if err := r.client.Status().Update(ctx, clusterQueue); err != nil {
				return fmt.Errorf("updating ClusterQueue %q effectiveQuotas: %w", clusterQueue.Name, err)
			}
		}
	}

	targetCohortNames := sets.New[string]()
	for i := range targetCohorts {
		cohort := &targetCohorts[i]
		targetCohortNames.Insert(cohort.Name)
		newEffectiveQuotas := buildEffectiveQuotas(orchestratorName, cohort.Spec.ResourceGroups, allocatedQuantities, getNodeID(true, cohort.Name))
		if !equality.Semantic.DeepEqual(cohort.Status.EffectiveQuotas, newEffectiveQuotas) {
			cohort.Status.EffectiveQuotas = newEffectiveQuotas
			if err := r.client.Status().Update(ctx, cohort); err != nil {
				return fmt.Errorf("updating Cohort %q effectiveQuotas: %w", cohort.Name, err)
			}
		}
	}

	var previouslyManagedCQs kueue.ClusterQueueList
	if err := r.client.List(ctx, &previouslyManagedCQs, client.MatchingFields{
		indexer.ClusterQueueEffectiveQuotaOrchestratorKey: orchestratorName,
	}); err != nil {
		return fmt.Errorf("listing previously managed ClusterQueues: %w", err)
	}
	for i := range previouslyManagedCQs.Items {
		cq := &previouslyManagedCQs.Items[i]
		if !targetCQNames.Has(cq.Name) {
			cq.Status.EffectiveQuotas = nil
			if err := r.client.Status().Update(ctx, cq); err != nil {
				return fmt.Errorf("clearing orphaned effectiveQuotas on ClusterQueue %q: %w", cq.Name, err)
			}
		}
	}

	var previouslyManagedCohorts kueue.CohortList
	if err := r.client.List(ctx, &previouslyManagedCohorts, client.MatchingFields{
		indexer.CohortEffectiveQuotaOrchestratorKey: orchestratorName,
	}); err != nil {
		return fmt.Errorf("listing previously managed Cohorts: %w", err)
	}
	for i := range previouslyManagedCohorts.Items {
		cohort := &previouslyManagedCohorts.Items[i]
		if !targetCohortNames.Has(cohort.Name) {
			cohort.Status.EffectiveQuotas = nil
			if err := r.client.Status().Update(ctx, cohort); err != nil {
				return fmt.Errorf("clearing orphaned effectiveQuotas on Cohort %q: %w", cohort.Name, err)
			}
		}
	}

	return nil
}

// isOtherActiveDistributingDQO returns true if the referenced orchestrator is an active distributing DQO other than the one being reconciled.
func isOtherActiveDistributingDQO(ref kueue.EffectiveQuotaStatusOrchestratorRef, allOrchestrators []kueuealpha.DynamicQuotaOrchestrator) bool {
	if ref.Kind != kueuealpha.DynamicQuotaOrchestratorKind || (ref.APIGroup != "" && ref.APIGroup != kueuealpha.SchemeGroupVersion.Group) {
		return true
	}
	for _, other := range allOrchestrators {
		if other.DeletionTimestamp.IsZero() && other.Name == ref.Name && other.Spec.CapacityDistribution != nil {
			distCond := apimeta.FindStatusCondition(other.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
			if distCond != nil && distCond.Status == metav1.ConditionFalse {
				return false
			}
			return true
		}
	}
	return false
}

type quotaKey struct {
	flavor   kueuealpha.ResourceFlavorReference
	resource corev1.ResourceName
}

type participantNode struct {
	id               string
	isCohort         bool
	name             string
	specNominalQuota resource.Quantity
}

// getNodeID returns a canonical identifier string for a participant Cohort or ClusterQueue.
func getNodeID(isCohort bool, name string) string {
	if isCohort {
		return "Cohort/" + name
	}
	return "ClusterQueue/" + name
}

// collectParticipants finds all Cohorts and ClusterQueues in the subtree that define a nominal quota for the given flavor and resource.
func collectParticipants(
	flavor kueuealpha.ResourceFlavorReference,
	resourceName corev1.ResourceName,
	cohorts []kueue.Cohort,
	clusterQueues []kueue.ClusterQueue,
) []participantNode {
	var participants []participantNode

	for _, cohort := range cohorts {
		if quota, found := findNominalQuota(cohort.Spec.ResourceGroups, flavor, resourceName); found {
			participants = append(participants, participantNode{
				id:               getNodeID(true, cohort.Name),
				isCohort:         true,
				name:             cohort.Name,
				specNominalQuota: quota,
			})
		}
	}

	for _, clusterQueue := range clusterQueues {
		if quota, found := findNominalQuota(clusterQueue.Spec.ResourceGroups, flavor, resourceName); found {
			participants = append(participants, participantNode{
				id:               getNodeID(false, clusterQueue.Name),
				isCohort:         false,
				name:             clusterQueue.Name,
				specNominalQuota: quota,
			})
		}
	}

	return participants
}

// findNominalQuota searches resource groups for the nominal quota of a specific flavor and resource.
func findNominalQuota(
	resourceGroups []kueue.ResourceGroup,
	flavor kueuealpha.ResourceFlavorReference,
	resourceName corev1.ResourceName,
) (resource.Quantity, bool) {
	for _, rg := range resourceGroups {
		for _, f := range rg.Flavors {
			if string(f.Name) == string(flavor) {
				for _, r := range f.Resources {
					if r.Name == resourceName {
						return r.NominalQuota, true
					}
				}
			}
		}
	}
	return resource.Quantity{}, false
}

type remainderEntry struct {
	participant participantNode
	floor       *inf.Dec
	remainder   *inf.Dec
}

// distributeCapacityProportionally allocates the total capacity across participants in proportion to their nominal quotas using the largest remainder method.
func distributeCapacityProportionally(
	capacity resource.Quantity,
	participants []participantNode,
) map[string]resource.Quantity {
	result := make(map[string]resource.Quantity, len(participants))
	if len(participants) == 0 {
		return result
	}

	sumSpecNominalQuota := new(inf.Dec)
	for _, p := range participants {
		sumSpecNominalQuota.Add(sumSpecNominalQuota, p.specNominalQuota.AsDec())
	}

	if sumSpecNominalQuota.Sign() <= 0 {
		for _, p := range participants {
			result[p.id] = *resource.NewQuantity(0, capacity.Format)
		}
		return result
	}

	capacityDec := capacity.AsDec()
	scale := max(0, capacityDec.Scale())
	unitDec := inf.NewDec(1, scale)

	entries := make([]remainderEntry, len(participants))
	sumFloors := new(inf.Dec)

	for i, p := range participants {
		ideal := new(inf.Dec).Mul(capacityDec, p.specNominalQuota.AsDec())
		ideal.QuoRound(ideal, sumSpecNominalQuota, scale+9, inf.RoundDown)

		floor := new(inf.Dec).Round(ideal, scale, inf.RoundDown)
		remainder := new(inf.Dec).Sub(ideal, floor)

		entries[i] = remainderEntry{participant: p, floor: floor, remainder: remainder}
		sumFloors.Add(sumFloors, floor)
	}

	diff := new(inf.Dec).Sub(capacityDec, sumFloors)
	stepsLeft := int(new(inf.Dec).QuoRound(diff, unitDec, 0, inf.RoundHalfUp).UnscaledBig().Int64())

	slices.SortFunc(entries, func(a, b remainderEntry) int {
		if cmp := b.remainder.Cmp(a.remainder); cmp != 0 {
			return cmp
		}
		if a.participant.isCohort != b.participant.isCohort {
			if a.participant.isCohort {
				return -1
			}
			return 1
		}
		return strings.Compare(a.participant.name, b.participant.name)
	})

	for i, entry := range entries {
		allocated := entry.floor
		if i < stepsLeft {
			allocated = new(inf.Dec).Add(allocated, unitDec)
		}
		result[entry.participant.id] = *resource.NewDecimalQuantity(*allocated, capacity.Format)
	}

	return result
}

// buildEffectiveQuotas creates an EffectiveQuotaStatus for a node with its allocated nominal quotas.
func buildEffectiveQuotas(
	orchestratorName string,
	specResourceGroups []kueue.ResourceGroup,
	allocatedQuantities map[quotaKey]map[string]resource.Quantity,
	nodeID string,
) *kueue.EffectiveQuotaStatus {
	if len(specResourceGroups) == 0 {
		return nil
	}
	effectiveResourceGroups := make([]kueue.ResourceGroup, len(specResourceGroups))
	for i, resourceGroup := range specResourceGroups {
		effectiveResourceGroups[i] = *resourceGroup.DeepCopy()
		for j, flavorQuotas := range effectiveResourceGroups[i].Flavors {
			for k, resourceQuota := range flavorQuotas.Resources {
				key := quotaKey{
					flavor:   kueuealpha.ResourceFlavorReference(flavorQuotas.Name),
					resource: resourceQuota.Name,
				}
				if nodeAllocations, found := allocatedQuantities[key]; found {
					if allocated, ok := nodeAllocations[nodeID]; ok {
						effectiveResourceGroups[i].Flavors[j].Resources[k].NominalQuota = allocated
					}
				}
			}
		}
	}

	return &kueue.EffectiveQuotaStatus{
		OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
			APIGroup: kueuealpha.SchemeGroupVersion.Group,
			Kind:     kueuealpha.DynamicQuotaOrchestratorKind,
			Name:     orchestratorName,
		},
		ResourceGroups: effectiveResourceGroups,
	}
}

// resolveSubtree traverses the quota hierarchy downwards from the specified root reference to find all member ClusterQueues and Cohorts.
func (r *DynamicQuotaOrchestratorReconciler) resolveSubtree(
	ctx context.Context,
	rootRef kueuealpha.CapacityDistributionSubtreeRootRef,
) ([]kueue.ClusterQueue, []kueue.Cohort, error) {
	if rootRef.Kind == kueuealpha.ClusterQueueSubtreeRootRefKind {
		var clusterQueue kueue.ClusterQueue
		if err := r.client.Get(ctx, types.NamespacedName{Name: rootRef.Name}, &clusterQueue); err != nil {
			return nil, nil, err
		}
		return []kueue.ClusterQueue{clusterQueue}, nil, nil
	}

	if rootRef.Kind == kueuealpha.CohortSubtreeRootRefKind {
		var rootCohort kueue.Cohort
		if err := r.client.Get(ctx, types.NamespacedName{Name: rootRef.Name}, &rootCohort); err != nil {
			return nil, nil, err
		}

		targetCohorts := []kueue.Cohort{rootCohort}
		var targetClusterQueues []kueue.ClusterQueue
		cohortQueue := []string{rootCohort.Name}
		visitedCohorts := sets.New(rootCohort.Name)

		for len(cohortQueue) > 0 {
			currentCohortName := cohortQueue[0]
			cohortQueue = cohortQueue[1:]

			// Find direct ClusterQueues under this cohort using indexer
			var cqList kueue.ClusterQueueList
			if err := r.client.List(ctx, &cqList, client.MatchingFields{
				indexer.ClusterQueueCohortKey: currentCohortName,
			}); err != nil {
				return nil, nil, err
			}
			targetClusterQueues = append(targetClusterQueues, cqList.Items...)

			// Find direct child Cohorts under this cohort using indexer
			var childCohortList kueue.CohortList
			if err := r.client.List(ctx, &childCohortList, client.MatchingFields{
				indexer.CohortParentKey: currentCohortName,
			}); err != nil {
				return nil, nil, err
			}
			for _, child := range childCohortList.Items {
				if !visitedCohorts.Has(child.Name) {
					visitedCohorts.Insert(child.Name)
					targetCohorts = append(targetCohorts, child)
					cohortQueue = append(cohortQueue, child.Name)
				}
			}
		}

		return targetClusterQueues, targetCohorts, nil
	}

	return nil, nil, fmt.Errorf("unsupported subtree root kind %q", rootRef.Kind)
}

// isStrictAncestor returns true if candidate is a strict ancestor of target in the cohort hierarchy.
func (r *DynamicQuotaOrchestratorReconciler) isStrictAncestor(
	ctx context.Context,
	candidate kueuealpha.CapacityDistributionSubtreeRootRef,
	target kueuealpha.CapacityDistributionSubtreeRootRef,
) (bool, error) {
	if candidate.Kind != kueuealpha.CohortSubtreeRootRefKind || candidate == target {
		return false, nil
	}

	currentName := target.Name
	if target.Kind == kueuealpha.ClusterQueueSubtreeRootRefKind {
		var cq kueue.ClusterQueue
		if err := r.client.Get(ctx, types.NamespacedName{Name: target.Name}, &cq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if cq.Spec.CohortName == "" {
			return false, nil
		}
		if string(cq.Spec.CohortName) == candidate.Name {
			return true, nil
		}
		currentName = string(cq.Spec.CohortName)
	}

	visited := sets.New(currentName)
	for currentName != "" {
		var cohort kueue.Cohort
		if err := r.client.Get(ctx, types.NamespacedName{Name: currentName}, &cohort); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		parentName := string(cohort.Spec.ParentName)
		if parentName == "" || visited.Has(parentName) {
			return false, nil
		}
		if parentName == candidate.Name {
			return true, nil
		}
		visited.Insert(parentName)
		currentName = parentName
	}

	return false, nil
}

// isOtherOlder returns true if other was created before current (breaking ties with UID).
func isOtherOlder(other, current *kueuealpha.DynamicQuotaOrchestrator) bool {
	if other.CreationTimestamp.Before(&current.CreationTimestamp) {
		return true
	}
	if current.CreationTimestamp.Before(&other.CreationTimestamp) {
		return false
	}
	return other.UID < current.UID
}

// setDiscoveryCondition sets the EffectiveCapacityComputed condition on the orchestrator status.
func (r *DynamicQuotaOrchestratorReconciler) setDiscoveryCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// setDistributionCondition sets the QuotasDistributed condition on the orchestrator status.
func (r *DynamicQuotaOrchestratorReconciler) setDistributionCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// updateStatus writes orchestrator.Status to etcd if it differs from oldStatus.
func (r *DynamicQuotaOrchestratorReconciler) updateStatus(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, oldStatus *kueuealpha.DynamicQuotaOrchestratorStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &orchestrator.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, orchestrator)
}
