package primitives

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var networkingResources = []struct{ apiVersion, kind string }{
	{"networking.k8s.io/v1", "Ingress"},
	{"gateway.networking.k8s.io/v1", "HTTPRoute"},
	{"gateway.networking.k8s.io/v1", "TLSRoute"},
	{"gateway.networking.k8s.io/v1", "BackendTLSPolicy"},
	{"monitoring.coreos.com/v1", "ServiceMonitor"},
	{"cilium.io/v2", "CiliumNetworkPolicy"},
}

func TestNetworkingDiscoveryAndTransferPreservePayload(t *testing.T) {
	for _, resource := range networkingResources {
		t.Run(resource.kind, func(t *testing.T) {
			metadata := discoveryTestManaged(resource.apiVersion, resource.kind, "networking")
			preview, err := DiscoverManagedResources(t.Context(),
				&discoveryTestReader{objects: []metav1.PartialObjectMetadata{metadata}}, discoveryTestOptions())
			require.NoError(t, err)
			require.Empty(t, preview.Blockers)
			require.Len(t, preview.Transfers, 1)

			request, original, objects := transferFixture()
			object := &unstructured.Unstructured{Object: map[string]any{
				"spec":   map[string]any{"selector": map[string]any{"app": "bao"}},
				"status": map[string]any{"observedGeneration": int64(1)},
			}}
			object.SetAPIVersion(resource.apiVersion)
			object.SetKind(resource.kind)
			object.SetName(original.Name)
			object.SetNamespace(original.Namespace)
			object.SetUID(original.UID)
			object.SetAnnotations(original.Annotations)
			object.SetOwnerReferences(original.OwnerReferences)
			if resource.kind == "CiliumNetworkPolicy" {
				object.Object["specs"] = []any{map[string]any{"description": "preserved"}}
			}
			request.Resource.APIVersion, request.Resource.Kind = resource.apiVersion, resource.kind
			objects[len(objects)-1] = object
			c := transferClient(t, objects, interceptor.Funcs{})
			before := object.DeepCopy()
			require.NoError(t, TransferOwnership(t.Context(), c, request))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(object), object))
			require.Equal(t, request.Destination.UID, object.GetOwnerReferences()[0].UID)
			request.Source, request.Destination = request.Destination, request.Source
			require.NoError(t, TransferOwnership(t.Context(), c, request))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(object), object))
			require.Equal(t, before.GetUID(), object.GetUID())
			require.Equal(t, before.Object["spec"], object.Object["spec"])
			require.Equal(t, before.Object["specs"], object.Object["specs"])
			require.Equal(t, before.Object["status"], object.Object["status"])
			require.Equal(t, before.GetOwnerReferences(), object.GetOwnerReferences())
		})
	}
}
