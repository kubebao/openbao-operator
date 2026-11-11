package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const networkCiliumKind = "CiliumNetworkPolicy"

var networkKinds = []schema.GroupVersionKind{
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "TLSRoute"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "BackendTLSPolicy"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"},
	{Group: "cilium.io", Version: "v2", Kind: networkCiliumKind},
}

func TestNetworkingHandoffBoundaries(t *testing.T) {
	policy := openedLockPolicy(t)
	for _, kind := range networkKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			before := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"unchanged": true}, "status": map[string]any{"ready": true},
			}}
			before.SetGroupVersionKind(kind)
			meta := managedMeta(testSourceAPI, testSourceUID)
			before.SetName(meta.Name)
			before.SetNamespace(meta.Namespace)
			before.SetLabels(meta.Labels)
			before.SetAnnotations(meta.Annotations)
			before.SetOwnerReferences(meta.OwnerReferences)
			if kind.Kind == networkCiliumKind {
				before.Object["specs"] = []any{map[string]any{"description": "preserved"}}
			}
			after := before.DeepCopy()
			meta = managedMeta(testDestAPI, testDestUID)
			after.SetAnnotations(meta.Annotations)
			after.SetOwnerReferences(meta.OwnerReferences)
			tc := handoffCase{operation: "UPDATE", kind: metav1.GroupVersionKind(kind), old: before, new: after}
			require.True(t, evaluateHandoff(t, policy, tc))
			tc.old, tc.new = after, before
			require.True(t, evaluateHandoff(t, policy, tc), "rollback must admit the same metadata-only swap")
			for _, field := range []string{"spec", "status", "specs"} {
				if field == "specs" && kind.Kind != networkCiliumKind {
					continue
				}
				changed := after.DeepCopy()
				changed.Object[field] = map[string]any{"changed": true}
				tc.old, tc.new = before, changed
				require.False(t, evaluateHandoff(t, policy, tc), "must reject %s changes", field)
			}
			tc.old, tc.new = before, after
			tc.kind.Group = "example.org"
			require.False(t, evaluateHandoff(t, policy, tc), "a matching kind in another group is not transferable")
		})
	}
}
