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

package capacityprovider

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		enableFeatureGate bool
		initObjects       []client.Object
		reqName           string
		wantCapacity      *kueuealpha.CapacityProviderNormalizedCapacity
		wantCondition     *metav1.Condition
	}{
		"feature gate disabled": {
			enableFeatureGate: false,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
			},
			reqName: "cp",
		},
		"different controllerName ignored": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName("custom.io/other-provider").
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
			},
			reqName: "cp",
		},
		"missing parameters": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionFalse,
				Reason:  kueuealpha.CapacityProviderReasonMisconfigured,
				Message: "spec.parameters is required",
			},
		},
		"unsupported parameters kind": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "InvalidKind", "cm").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonMisconfigured,
			},
		},
		"empty parameters name": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionFalse,
				Reason:  kueuealpha.CapacityProviderReasonMisconfigured,
				Message: "spec.parameters.name is required",
			},
		},
		"referenced ConfigMap not found": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "non-existent").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonMisconfigured,
			},
		},
		"missing capacity key in ConfigMap": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
				MakeCapacityConfigMap("cm", metav1.NamespaceDefault).
					RawData("other", "data").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonInvalidCapacity,
			},
		},
		"invalid capacity YAML in ConfigMap": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
				MakeCapacityConfigMap("cm", metav1.NamespaceDefault).
					RawData(CapacityConfigMapKey, "invalid: yaml: [").
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonInvalidCapacity,
			},
		},
		"negative quantity in capacity YAML": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
				MakeCapacityConfigMap("cm", metav1.NamespaceDefault).
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("-1"),
					}).
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonInvalidCapacity,
			},
		},
		"successful sync with multiple flavors": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1", "f2").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
				MakeCapacityConfigMap("cm", metav1.NamespaceDefault).
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10"),
						corev1.ResourceMemory: resource.MustParse("100Gi"),
					}).
					Flavor("f2", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("20"),
					}).
					Obj(),
			},
			reqName: "cp",
			wantCapacity: utiltestingalpha.MakeNormalizedCapacity().
				Flavor("f1", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10"),
					corev1.ResourceMemory: resource.MustParse("100Gi"),
				}).
				Flavor("f2", corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("20"),
				}).
				Obj(),
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionTrue,
				Reason: kueuealpha.CapacityProviderReasonSynchronized,
			},
		},
		"flavor mismatch between ConfigMap and CapacityProvider": {
			enableFeatureGate: true,
			initObjects: []client.Object{
				utiltestingalpha.MakeCapacityProvider("cp").
					ControllerName(kueuealpha.TestCapacityProviderControllerName).
					OrchestratedFlavors("f1", "f2").
					Parameters("k8s.io", "ConfigMap", "cm").
					Obj(),
				MakeCapacityConfigMap("cm", metav1.NamespaceDefault).
					Flavor("f1", corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("10"),
					}).
					Obj(),
			},
			reqName: "cp",
			wantCondition: &metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionFalse,
				Reason: kueuealpha.CapacityProviderReasonMisconfigured,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.enableFeatureGate)
			ctx := t.Context()
			client := utiltesting.NewFakeClient(tc.initObjects...)

			r := NewReconciler(client)
			_, err := r.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tc.reqName},
			})
			if err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			var gotCp kueuealpha.CapacityProvider
			if err := client.Get(ctx, types.NamespacedName{Name: tc.reqName}, &gotCp); err != nil {
				t.Fatalf("unexpected get error: %v", err)
			}

			if tc.wantCapacity != nil {
				if diff := cmp.Diff(tc.wantCapacity, gotCp.Status.Capacity); diff != "" {
					t.Errorf("unexpected capacity (-want +got):\n%s", diff)
				}
			}

			if tc.wantCondition != nil {
				gotCond := apimeta.FindStatusCondition(gotCp.Status.Conditions, tc.wantCondition.Type)
				if gotCond == nil {
					t.Errorf("expected condition %q not found in %v", tc.wantCondition.Type, gotCp.Status.Conditions)
				} else {
					if gotCond.Status != tc.wantCondition.Status {
						t.Errorf("condition status mismatch: want %v, got %v", tc.wantCondition.Status, gotCond.Status)
					}
					if gotCond.Reason != tc.wantCondition.Reason {
						t.Errorf("condition reason mismatch: want %v, got %v", tc.wantCondition.Reason, gotCond.Reason)
					}
					if tc.wantCondition.Message != "" && gotCond.Message != tc.wantCondition.Message {
						t.Errorf("condition message mismatch: want %v, got %v", tc.wantCondition.Message, gotCond.Message)
					}
				}
			}
		})
	}
}
