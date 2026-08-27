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

// Package capacityprovider provides a mock CapacityProvider controller for integration and e2e testing.
//
// The test CapacityProvider controller simulates an external capacity provider (e.g., cloud autoscaler,
// node capacity aggregator) by synchronizing capacity values from a Kubernetes ConfigMap into a CapacityProvider's
// Status.Capacity field.
//
// How to Use in Tests:
//
// 1. Register the reconciler in your test manager:
//
//	rec := capacityprovidertest.NewReconciler(mgr.GetClient())
//	err := rec.SetupWithManager(mgr)
//
// 2. Create a ConfigMap containing flavor capacity in YAML format under the "capacity" key:
//
//	cm := capacityprovidertest.MakeCapacityConfigMap("my-capacity-cm", ns.Name).
//		Flavor("default-flavor", corev1.ResourceList{
//			corev1.ResourceCPU:    resource.MustParse("100"),
//			corev1.ResourceMemory: resource.MustParse("50Gi"),
//		}).
//		Obj()
//
// 3. Create a CapacityProvider CR referencing this ConfigMap:
//
//	cp := utiltestingalpha.MakeCapacityProvider("my-cp").
//		ControllerName(kueuealpha.TestCapacityProviderControllerName).
//		OrchestratedFlavors("default-flavor").
//		Parameters("k8s.io", "ConfigMap", "my-capacity-cm").
//		Obj()
//
// 4. Update the ConfigMap dynamically during tests to simulate changing capacity:
//
//	cm.Data[capacityprovidertest.CapacityConfigMapKey] = capacityprovidertest.MakeCapacityConfig().
//		Flavor("default-flavor", corev1.ResourceList{
//			corev1.ResourceCPU: resource.MustParse("150"),
//		}).
//		MustMarshal()
//	client.Update(ctx, cm)
package capacityprovider

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/yaml"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
)

var errMultipleConfigMaps = errors.New("multiple ConfigMaps found across namespaces")

type Reconciler struct {
	client         client.Client
	controllerName kueuealpha.CapacityProviderControllerName
	namespace      string
}

type Option func(*Reconciler)

func WithControllerName(name kueuealpha.CapacityProviderControllerName) Option {
	return func(r *Reconciler) {
		r.controllerName = name
	}
}

func WithNamespace(ns string) Option {
	return func(r *Reconciler) {
		r.namespace = ns
	}
}

func NewReconciler(client client.Client, opts ...Option) *Reconciler {
	r := &Reconciler{
		client:         client,
		controllerName: kueuealpha.TestCapacityProviderControllerName,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.CapacityProvider{}).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.mapConfigMapToCapacityProviders),
		).
		Complete(r)
}

func (r *Reconciler) mapConfigMapToCapacityProviders(ctx context.Context, obj client.Object) []ctrl.Request {
	configMap, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}
	if r.namespace != "" && configMap.Namespace != r.namespace {
		return nil
	}
	var cpList kueuealpha.CapacityProviderList
	if err := r.client.List(ctx, &cpList); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list CapacityProviders for ConfigMap", "configMap", configMap.Name)
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(cpList.Items))
	for _, cp := range cpList.Items {
		if cp.Spec.ControllerName == r.controllerName && cp.Spec.Parameters != nil && cp.Spec.Parameters.Name == configMap.Name {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: cp.Name},
			})
		}
	}
	return reqs
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DynamicQuotaOrchestration) {
		return ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconciling CapacityProvider", "capacityProvider", req.NamespacedName)

	var cp kueuealpha.CapacityProvider
	if err := r.client.Get(ctx, req.NamespacedName, &cp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if cp.Spec.ControllerName != r.controllerName {
		log.V(5).Info("Ignoring CapacityProvider managed by another controller", "controllerName", cp.Spec.ControllerName)
		return ctrl.Result{}, nil
	}

	oldStatus := cp.Status.DeepCopy()

	if cp.Spec.Parameters == nil {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonMisconfigured, "spec.parameters is required")
	}

	if cp.Spec.Parameters.Kind != "ConfigMap" {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonMisconfigured, fmt.Sprintf("Unsupported parameters: expected Kind %q, got %q", "ConfigMap", cp.Spec.Parameters.Kind))
	}

	if cp.Spec.Parameters.Name == "" {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonMisconfigured, "spec.parameters.name is required")
	}

	configMap, err := r.findConfigMap(ctx, cp.Spec.Parameters.Name)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, errMultipleConfigMaps) {
			return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonMisconfigured, err.Error())
		}
		return r.failSyncWithErr(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonSourceUnavailable, err)
	}

	capacityYAML, ok := configMap.Data[CapacityConfigMapKey]
	if !ok || capacityYAML == "" {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonInvalidCapacity, fmt.Sprintf("ConfigMap data is missing %q key", CapacityConfigMapKey))
	}

	var parsedData TestCapacityConfig
	if err := yaml.Unmarshal([]byte(capacityYAML), &parsedData); err != nil {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonInvalidCapacity, fmt.Sprintf("Failed to parse capacity YAML: %v", err))
	}

	for _, f := range parsedData.Flavors {
		for rName, qty := range f.Resources {
			if qty.Sign() < 0 {
				return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonInvalidCapacity, fmt.Sprintf("Negative quantity %v for resource %q in flavor %q", qty.String(), rName, f.Name))
			}
		}
	}

	orchestrated := sets.New[kueuealpha.ResourceFlavorReference]()
	for _, of := range cp.Spec.OrchestratedFlavors {
		orchestrated.Insert(of.Name)
	}

	configMapFlavors := sets.New[kueuealpha.ResourceFlavorReference]()
	normalizedFlavors := make([]kueuealpha.CapacityProviderNormalizedCapacityFlavor, 0, len(parsedData.Flavors))
	for _, f := range parsedData.Flavors {
		configMapFlavors.Insert(f.Name)
		normalizedFlavors = append(normalizedFlavors, kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			Name:      f.Name,
			Resources: f.Resources.DeepCopy(),
		})
	}

	if !orchestrated.Equal(configMapFlavors) {
		return r.failSync(ctx, &cp, oldStatus, kueuealpha.CapacityProviderReasonMisconfigured,
			fmt.Sprintf("ConfigMap flavors %v do not match CapacityProvider orchestrated flavors %v", sets.List(configMapFlavors), sets.List(orchestrated)))
	}

	cp.Status.Capacity = &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: normalizedFlavors,
	}
	apimeta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.CapacityProviderCapacitySynchronized,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: cp.Generation,
		Reason:             kueuealpha.CapacityProviderReasonSynchronized,
		Message:            "Capacity synchronized successfully",
	})

	return ctrl.Result{}, r.updateStatus(ctx, &cp, oldStatus)
}

func (r *Reconciler) findConfigMap(ctx context.Context, name string) (*corev1.ConfigMap, error) {
	if r.namespace != "" {
		cmKey := types.NamespacedName{Namespace: r.namespace, Name: name}
		var configMap corev1.ConfigMap
		if err := r.client.Get(ctx, cmKey, &configMap); err != nil {
			return nil, err
		}
		return &configMap, nil
	}

	var cmList corev1.ConfigMapList
	if err := r.client.List(ctx, &cmList); err != nil {
		return nil, err
	}
	var matching []corev1.ConfigMap
	for _, item := range cmList.Items {
		if item.Name == name {
			matching = append(matching, item)
		}
	}
	if len(matching) == 0 {
		return nil, apierrors.NewNotFound(corev1.Resource("configmaps"), name)
	}
	if len(matching) > 1 {
		return nil, fmt.Errorf("%w with name %q", errMultipleConfigMaps, name)
	}
	return &matching[0], nil
}

func (r *Reconciler) failSync(ctx context.Context, cp *kueuealpha.CapacityProvider, oldStatus *kueuealpha.CapacityProviderStatus, reason, message string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.CapacityProviderCapacitySynchronized,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: cp.Generation,
		Reason:             reason,
		Message:            message,
	})
	cp.Status.Capacity = nil
	return ctrl.Result{}, r.updateStatus(ctx, cp, oldStatus)
}

func (r *Reconciler) failSyncWithErr(ctx context.Context, cp *kueuealpha.CapacityProvider, oldStatus *kueuealpha.CapacityProviderStatus, reason string, syncErr error) (ctrl.Result, error) {
	_, updateErr := r.failSync(ctx, cp, oldStatus, reason, syncErr.Error())
	return ctrl.Result{}, errors.Join(syncErr, updateErr)
}

func (r *Reconciler) updateStatus(ctx context.Context, cp *kueuealpha.CapacityProvider, oldStatus *kueuealpha.CapacityProviderStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &cp.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, cp)
}
