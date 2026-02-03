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
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	nodeMultipleFailuresEvictionMessageFormat = "Workload eviction triggered due to multiple TAS assigned node failures, including: %s"
)

// nodeFailureReconciler reconciles Nodes to detect failures and update affected Workloads
type nodeFailureReconciler struct {
	client      client.Client
	clock       clock.Clock
	log         logr.Logger
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
}

func (r *nodeFailureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == "" {
		return ctrl.Result{}, nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconcile Node triggered", "node", req.Name)

	var node corev1.Node
	var affectedWorkloads sets.Set[types.NamespacedName]

	err := r.client.Get(ctx, req.NamespacedName, &node)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	nodeExists := err == nil

	var errMsg string
	if !nodeExists {
		log.Info("Node not found", "node", req.Name)
		errMsg = "Node not found"
	} else if utiltas.GetNodeCondition(&node, corev1.NodeReady) == nil {
		log.Info("NodeReady condition is missing", "node", req.Name)
		errMsg = "NodeReady condition is missing"
	}

	if errMsg != "" {
		log.Info("Node error detected", "error", errMsg, "node", req.Name)
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			log.Error(err, "Failed to get workloads on node", "node", req.Name)
			return ctrl.Result{}, err
		}
		log.Info("Handling unhealthy node", "node", req.Name, "affectedWorkloadsCount", len(affectedWorkloads))
		return ctrl.Result{}, r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
	}

	readyCondition := utiltas.GetNodeCondition(&node, corev1.NodeReady)
	if readyCondition.Status == corev1.ConditionTrue {
		log.Info("Node is Ready", "node", req.Name)
		affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
		if err != nil {
			log.Error(err, "Failed to get workloads on node", "node", req.Name)
			return ctrl.Result{}, err
		}
		log.Info("Checking if valid ready node", "node", req.Name, "affectedWorkloadsCount", len(affectedWorkloads))
		err = r.handleReadyNode(ctx, &node, affectedWorkloads)
		if err != nil {
			log.Error(err, "Error in handleReadyNode", "node", req.Name)
			return ctrl.Result{}, err
		}
		log.Info("handleReadyNode finished", "node", req.Name)
		return ctrl.Result{}, nil
	}
	log.Info("Node is NotReady", "node", req.Name)
	if features.Enabled(features.TASReplaceNodeOnPodTermination) {
		return r.reconcileForReplaceNodeOnPodTermination(ctx, req.Name)
	}
	timeSinceNotReady := r.clock.Now().Sub(readyCondition.LastTransitionTime.Time)
	if NodeFailureDelay > timeSinceNotReady {
		return ctrl.Result{RequeueAfter: NodeFailureDelay - timeSinceNotReady}, nil
	}
	log.Info("Node is not ready and NodeFailureDelay timer expired, marking as failed", "node", req.Name)
	affectedWorkloads, err = r.getWorkloadsOnNode(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	patchErr := r.handleUnhealthyNode(ctx, req.Name, affectedWorkloads)
	return ctrl.Result{}, patchErr
}

var _ reconcile.Reconciler = (*nodeFailureReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Node] = (*nodeFailureReconciler)(nil)

func (r *nodeFailureReconciler) Generic(event.TypedGenericEvent[*corev1.Node]) bool {
	return false
}

func (r *nodeFailureReconciler) Create(e event.TypedCreateEvent[*corev1.Node]) bool {
	return true
}

func (r *nodeFailureReconciler) Update(e event.TypedUpdateEvent[*corev1.Node]) bool {
	newReady := utiltas.IsNodeStatusConditionTrue(e.ObjectNew.Status.Conditions, corev1.NodeReady)
	oldReady := utiltas.IsNodeStatusConditionTrue(e.ObjectOld.Status.Conditions, corev1.NodeReady)
	if oldReady != newReady {
		r.log.V(4).Info("Node Ready status changed, triggering reconcile", "node", klog.KObj(e.ObjectNew), "oldReady", oldReady, "newReady", newReady)
		return true
	}
	if !taintsEqual(e.ObjectOld.Spec.Taints, e.ObjectNew.Spec.Taints) {
		r.log.V(4).Info("Node taints changed, triggering reconcile", "node", klog.KObj(e.ObjectNew))
		return true
	}
	return false
}

func (r *nodeFailureReconciler) Delete(e event.TypedDeleteEvent[*corev1.Node]) bool {
	return true
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func newNodeFailureReconciler(client client.Client, recorder record.EventRecorder, roleTracker *roletracker.RoleTracker) *nodeFailureReconciler {
	return &nodeFailureReconciler{
		client:      client,
		log:         roletracker.WithReplicaRole(ctrl.Log.WithName(TASNodeFailureController), roleTracker),
		clock:       clock.RealClock{},
		recorder:    recorder,
		roleTracker: roleTracker,
	}
}

func (r *nodeFailureReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) (string, error) {
	return TASNodeFailureController, builder.ControllerManagedBy(mgr).
		Named("tas_node_failure_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Node{},
			&handler.TypedEnqueueRequestForObject[*corev1.Node]{},
			r,
		)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
			pod := o.(*corev1.Pod)
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}}}
		})).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Node").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "tas-node-failure-reconciler"),
		}).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Node{}, cfg))
}

// getWorkloadsOnNode gets all workloads that have the given node assigned in TAS topology assignment
func (r *nodeFailureReconciler) getWorkloadsOnNode(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	var allWorkloads kueue.WorkloadList
	if err := r.client.List(ctx, &allWorkloads); err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}
	tasWorkloadsOnNode := sets.New[types.NamespacedName]()
	for _, wl := range allWorkloads.Items {
		if hasTASAssignmentOnNode(&wl, nodeName) {
			tasWorkloadsOnNode.Insert(types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace})
		}
	}
	ctrl.LoggerFrom(ctx).Info("getWorkloadsOnNode", "node", nodeName, "foundCount", tasWorkloadsOnNode.Len())
	return tasWorkloadsOnNode, nil
}

func hasTASAssignmentOnNode(wl *kueue.Workload, nodeName string) bool {
	if !isAdmittedByTAS(wl) {
		return false
	}
	for _, podSetAssignment := range wl.Status.Admission.PodSetAssignments {
		topologyAssignment := podSetAssignment.TopologyAssignment
		if topologyAssignment == nil {
			continue
		}
		if !utiltas.IsLowestLevelHostname(topologyAssignment.Levels) {
			continue
		}
		for value := range utiltas.LowestLevelValues(topologyAssignment) {
			if value == nodeName {
				return true
			}
		}
	}
	return false
}

func (r *nodeFailureReconciler) getWorkloadsForImmediateReplacement(ctx context.Context, nodeName string) (sets.Set[types.NamespacedName], error) {
	tasWorkloadsOnNode, err := r.getWorkloadsOnNode(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to list all workloads on node: %w", err)
	}

	affectedWorkloads := sets.New[types.NamespacedName]()
	for wlKey := range tasWorkloadsOnNode {
		var podsForWl corev1.PodList
		if err := r.client.List(ctx, &podsForWl, client.InNamespace(wlKey.Namespace), client.MatchingFields{indexer.WorkloadNameKey: wlKey.Name}); err != nil {
			return nil, fmt.Errorf("failed to list pods for workload %s: %w", wlKey, err)
		}
		shouldReplace := false
		hasPodsOnNode := false
		anyRunning := false
		for _, pod := range podsForWl.Items {
			if pod.Spec.NodeName == nodeName {
				hasPodsOnNode = true
				isTerminating := pod.DeletionTimestamp != nil
				isTerminated := utilpod.IsTerminated(&pod)
				if !isTerminating && !isTerminated {
					anyRunning = true
					break
				}
			}
		}
		if hasPodsOnNode && !anyRunning {
			shouldReplace = true
		}
		if shouldReplace {
			affectedWorkloads.Insert(wlKey)
		}
	}
	return affectedWorkloads, nil
}

// evictWorkloadIfNeeded idempotently evicts the workload when the node has failed.
// It returns whether the node was evicted, and whether an error was encountered.
func (r *nodeFailureReconciler) evictWorkloadIfNeeded(ctx context.Context, wl *kueue.Workload, nodeName string) (bool, error) {
	if workload.HasUnhealthyNodes(wl) && !workload.HasUnhealthyNode(wl, nodeName) && !workload.IsEvicted(wl) {
		unhealthyNodeNames := workload.UnhealthyNodeNames(wl)
		log := ctrl.LoggerFrom(ctx).WithValues("unhealthyNodes", unhealthyNodeNames)
		log.V(3).Info("Evicting workload due to multiple node failures")
		allUnhealthyNodeNames := append(unhealthyNodeNames, nodeName)
		evictionMsg := fmt.Sprintf(nodeMultipleFailuresEvictionMessageFormat, strings.Join(allUnhealthyNodeNames, ", "))
		if evictionErr := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedDueToNodeFailures, evictionMsg, "", r.clock, r.roleTracker); evictionErr != nil {
			log.Error(evictionErr, "Failed to complete eviction process")
			return false, evictionErr
		} else {
			return true, nil
		}
	}
	return false, nil
}

// handleUnhealthyNode finds workloads with pods on the specified node
// and patches their status to indicate the node is to replace.
func (r *nodeFailureReconciler) handleUnhealthyNode(ctx context.Context, nodeName string, affectedWorkloads sets.Set[types.NamespacedName]) error {
	log := ctrl.LoggerFrom(ctx)
	var workloadProcessingErrors []error
	for wlKey := range affectedWorkloads {
		log = log.WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))
		// fetch workload.
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(4).Info("Workload not found, skipping")
			} else {
				log.Error(err, "Failed to get workload")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}
		// evict workload when workload already has a different node marked for replacement
		evictedNow, err := r.evictWorkloadIfNeeded(ctx, &wl, nodeName)
		if err != nil {
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}
		if !evictedNow && !workload.IsEvicted(&wl) {
			if err := r.addUnhealthyNode(ctx, &wl, nodeName); err != nil {
				log.Error(err, "Failed to add node to unhealthyNodes")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) reconcileForReplaceNodeOnPodTermination(ctx context.Context, nodeName string) (ctrl.Result, error) {
	workloads, err := r.getWorkloadsForImmediateReplacement(ctx, nodeName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("could not get workloads for immediate replacement on node %s: %w", nodeName, err)
	}
	if len(workloads) == 0 {
		return ctrl.Result{}, nil
	}

	ctrl.LoggerFrom(ctx).V(3).Info("Node is not ready and has only terminating or failed pods. Marking as failed immediately")
	patchErr := r.handleUnhealthyNode(ctx, nodeName, workloads)
	return ctrl.Result{}, patchErr
}

// handleReadyNode checks if the node interacts with the workloads in a healthy way.
// It checks for taints that might make the node unusable for the workloads.
func (r *nodeFailureReconciler) handleReadyNode(ctx context.Context, node *corev1.Node, affectedWorkloads sets.Set[types.NamespacedName]) error {
	log := ctrl.LoggerFrom(ctx)
	var workloadProcessingErrors []error

	for wlKey := range affectedWorkloads {
		log = log.WithValues("workload", klog.KRef(wlKey.Namespace, wlKey.Name))

		log.Info("Checking workload", "workload", wlKey)
		var wl kueue.Workload
		if err := r.client.Get(ctx, wlKey, &wl); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("Workload not found, skipping", "workload", wlKey)
			} else {
				log.Error(err, "Failed to get workload", "workload", wlKey)
				workloadProcessingErrors = append(workloadProcessingErrors, err)
			}
			continue
		}

		isUnhealthy, _, err := r.isNodeUnhealthyForWorkload(ctx, node, &wl)
		log.Info("isNodeUnhealthyForWorkload result", "workload", wlKey, "isUnhealthy", isUnhealthy, "error", err)
		if err != nil {
			log.Error(err, "Failed to check if node is unhealthy for workload")
			workloadProcessingErrors = append(workloadProcessingErrors, err)
			continue
		}

		if isUnhealthy {
			// evict workload when workload already has a different node marked for replacement
			evictedNow, err := r.evictWorkloadIfNeeded(ctx, &wl, node.Name)
			if err != nil {
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
			if !evictedNow && !workload.IsEvicted(&wl) {
				if err := r.addUnhealthyNode(ctx, &wl, node.Name); err != nil {
					log.Error(err, "Failed to add node to unhealthyNodes")
					workloadProcessingErrors = append(workloadProcessingErrors, err)
					continue
				}
				log.Info("Added node to unhealthyNodes", "workload", wl.Name, "node", node.Name)
			}
		} else {
			if !slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: node.Name}) {
				continue
			}

			log.V(4).Info("Remove node from unhealthyNodes")
			if err := r.removeUnhealthyNodes(ctx, &wl, node.Name); err != nil {
				log.Error(err, "Failed to patch workload status")
				workloadProcessingErrors = append(workloadProcessingErrors, err)
				continue
			}
			log.V(3).Info("Successfully removed node from the unhealthyNodes field")
		}
	}
	if len(workloadProcessingErrors) > 0 {
		return errors.Join(workloadProcessingErrors...)
	}
	return nil
}

func (r *nodeFailureReconciler) isNodeUnhealthyForWorkload(ctx context.Context, node *corev1.Node, wl *kueue.Workload) (bool, bool, error) {
	taints := node.Spec.Taints
	if len(taints) == 0 {
		ctrl.LoggerFrom(ctx).Info("Node has no taints", "node", node.Name)
		return false, false, nil
	}
	// We only care about untolerated taints.
	// We assume that all pods in the workload have the same tolerations.
	untoleratedTaints := make([]corev1.Taint, 0, len(taints))
	toleratedNoExecuteTaints := make([]corev1.Taint, 0, len(taints))

	// We can check the first podset assignment to get the tolerations, or just check the pod spec if we have it?
	// Workload does not store tolerations in Status. We need to look at the PodSets in Spec.
	// But PodSets in Spec are templates.
	// Simplest is to check against all PodSets in the workload. If any PodSet that is assigned to this node
	// does NOT tolerate the taint, then it's untolerated.

	podsAreAssigned := false
	var podSetsToCheck []kueue.PodSet
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		// check if this assignment includes this node
		assigned := false
		if psa.TopologyAssignment != nil && utiltas.IsLowestLevelHostname(psa.TopologyAssignment.Levels) {
			for val := range utiltas.LowestLevelValues(psa.TopologyAssignment) {
				if val == node.Name {
					assigned = true
					break
				}
			}
		}
		if assigned {
			podsAreAssigned = true
			// Find the PodSet in Spec
			for _, ps := range wl.Spec.PodSets {
				if ps.Name == psa.Name {
					podSetsToCheck = append(podSetsToCheck, ps)
					break
				}
			}
		}
	}

	ctrl.LoggerFrom(ctx).Info("Checked pod assignments", "node", node.Name, "workload", wl.Name, "podsAreAssigned", podsAreAssigned)

	if !podsAreAssigned {
		// weird, it shouldn't happen if we got here via getWorkloadsOnNode
		return false, false, nil
	}

	for _, t := range taints {
		if t.Effect == corev1.TaintEffectPreferNoSchedule {
			continue
		}
		tolerated := true
		for _, ps := range podSetsToCheck {
			toleratedByPS := false
			for _, tol := range ps.Template.Spec.Tolerations {
				if tol.ToleratesTaint(klog.Background(), &t, true) {
					toleratedByPS = true
					break
				}
			}
			if !toleratedByPS {
				tolerated = false
				break
			}
		}
		if !tolerated {
			untoleratedTaints = append(untoleratedTaints, t)
		} else if t.Effect == corev1.TaintEffectNoExecute {
			toleratedNoExecuteTaints = append(toleratedNoExecuteTaints, t)
		}
	}
	ctrl.LoggerFrom(ctx).Info("Taints check", "workload", wl.Name, "untolerated", len(untoleratedTaints), "toleratedNoExecute", len(toleratedNoExecuteTaints))

	if len(untoleratedTaints) == 0 && len(toleratedNoExecuteTaints) == 0 {
		return false, false, nil
	}

	// Fetch pods
	var podsForWl corev1.PodList
	if err := r.client.List(ctx, &podsForWl, client.InNamespace(wl.Namespace), client.MatchingFields{indexer.WorkloadNameKey: wl.Name}); err != nil {
		return false, false, fmt.Errorf("list pods: %w", err)
	}

	// Filter pods for this node
	var podsOnNode []corev1.Pod
	for _, pod := range podsForWl.Items {
		if pod.Spec.NodeName == node.Name || (pod.Spec.NodeName == "" && pod.Spec.NodeSelector[corev1.LabelHostname] == node.Name) {
			podsOnNode = append(podsOnNode, pod)
		}
	}
	ctrl.LoggerFrom(ctx).Info("Pods on node count", "workload", wl.Name, "node", node.Name, "count", len(podsOnNode))

	// 1. Check Untolerated NoExecute (Immediate Unhealthy)
	for _, t := range untoleratedTaints {
		if t.Effect == corev1.TaintEffectNoExecute {
			return true, false, nil
		}
	}

	// 2. Check Tolerated NoExecute (Unhealthy only if pods Terminating)
	if len(toleratedNoExecuteTaints) > 0 {
		if len(podsOnNode) == 0 {
			// If pods are missing but we expect them, and we have NoExecute...
			// Maybe they were already evicted?
			// If Untolerated NoSchedule also exists, we might return Unhealthy below.
			// But if solely Tolerated NoExecute?
			// If pods are gone, we are technically "Healthy" (node empty of our pods), but workload might be failing.
			// However, `isNodeUnhealthyForWorkload` checks if the NODE is bad for the WORKLOAD.
			// If pods are gone, replacement probably happened or isn't needed here?
			// Let's assume return false (Healthy) if pods are gone, or let NoSchedule check handle it.
		} else {
			for _, pod := range podsOnNode {
				if pod.DeletionTimestamp != nil {
					return true, false, nil
				}
			}
			// If not terminating, we wait.
		}
	}

	// 3. Check Untolerated NoSchedule
	hasUntoleratedNoSchedule := false
	for _, t := range untoleratedTaints {
		if t.Effect == corev1.TaintEffectNoSchedule {
			hasUntoleratedNoSchedule = true
			break
		}
	}

	if hasUntoleratedNoSchedule {
		if len(podsOnNode) == 0 {
			// Pods absent and NoSchedule -> Unhealthy (cannot start)
			return true, false, nil
		}
		allRunning := true
		for _, pod := range podsOnNode {
			if pod.Status.Phase != corev1.PodRunning {
				allRunning = false
				break
			}
			if pod.DeletionTimestamp != nil {
				// Terminating is always bad if we have untolerated taints
				return true, false, nil
			}
		}
		if allRunning {
			// Pods running despite NoSchedule -> KeepMonitoring (wait for failure)
			return false, true, nil
		} else {
			// Pods not running -> Unhealthy
			return true, false, nil
		}
	}

	return false, false, nil
}

func (r *nodeFailureReconciler) removeUnhealthyNodes(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if workload.HasUnhealthyNode(wl, nodeName) {
		return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.UnhealthyNodes = slices.DeleteFunc(wl.Status.UnhealthyNodes, func(n kueue.UnhealthyNode) bool {
				return n.Name == nodeName
			})
			ctrl.LoggerFrom(ctx).Info("Successfully removed node from unhealthyNodes", "node", nodeName, "workload", klog.KObj(wl))
			return true, nil
		})
	}
	return nil
}

func (r *nodeFailureReconciler) addUnhealthyNode(ctx context.Context, wl *kueue.Workload, nodeName string) error {
	if !workload.HasUnhealthyNode(wl, nodeName) {
		return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.UnhealthyNodes = append(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName})
			ctrl.LoggerFrom(ctx).Info("Successfully added node to unhealthyNodes", "node", nodeName, "workload", klog.KObj(wl))
			return true, nil
		})
	}
	return nil
}

func taintsEqual(a, b []corev1.Taint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value || a[i].Effect != b[i].Effect || !a[i].TimeAdded.Equal(b[i].TimeAdded) {
			return false
		}
	}
	return true
}
