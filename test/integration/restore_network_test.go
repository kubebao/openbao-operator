//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/networking"
)

func TestRestoreTargetNetworkPreservesPreparedBoundary(t *testing.T) {
	for index, service := range []*api.ServiceConfig{nil, {}, {
		IPFamilyPolicy: ptr.To(corev1.IPFamilyPolicySingleStack), IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol},
	}} {
		t.Run(fmt.Sprintf("service-%d", index), func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := createMinimalCluster(t, namespace, "recovery")
			cluster.Annotations = map[string]string{constants.AnnotationRestoreOrigin: "request"}
			cluster.Spec.Service = service
			cluster.Spec.Network = &api.NetworkConfig{APIServerEgress: &api.APIServerEgressConfig{Mode: "Cilium"}}
			prepared := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "prepared-boundary", Namespace: namespace},
				Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}},
			}
			require.NoError(t, k8sClient.Create(ctx, prepared))
			original := prepared.DeepCopy()
			controller := newControllerClient(t)
			manager := networking.NewManager(controller, k8sScheme, "openbao-operator-system", constants.PlatformKubernetes)
			for range 2 {
				require.NoError(t, manager.Reconcile(ctx, logr.Discard(), cluster))
			}
			actual := &corev1.Service{}
			key := client.ObjectKey{Namespace: namespace, Name: cluster.Name + "-public"}
			err := k8sClient.Get(ctx, key, actual)
			if service == nil {
				require.True(t, apierrors.IsNotFound(err), "omitted configuration must preserve the internal-only target")
			} else {
				require.NoError(t, err)
				require.Equal(t, corev1.ServiceTypeClusterIP, actual.Spec.Type)
				if service.IPFamilyPolicy != nil {
					require.Equal(t, service.IPFamilyPolicy, actual.Spec.IPFamilyPolicy)
					require.Equal(t, service.IPFamilies, actual.Spec.IPFamilies)
				}
				require.Equal(t, constants.ComponentOpenBaoCluster, actual.Spec.Selector[constants.LabelOpenBaoComponent])
			}
			policies := &networkingv1.NetworkPolicyList{}
			require.NoError(t, k8sClient.List(ctx, policies, client.InNamespace(namespace)))
			require.Len(t, policies.Items, 1, "restore reconciliation must not add ordinary allow policies")
			require.Equal(t, original.UID, policies.Items[0].UID)
			require.Equal(t, original.Spec, policies.Items[0].Spec)
			cnps := &unstructured.UnstructuredList{}
			cnps.SetAPIVersion("cilium.io/v2")
			cnps.SetKind("CiliumNetworkPolicyList")
			require.NoError(t, k8sClient.List(ctx, cnps, client.InNamespace(namespace)))
			require.Empty(t, cnps.Items, "restore isolation must not gain Cilium API access")
			services := &corev1.ServiceList{}
			require.NoError(t, k8sClient.List(ctx, services, client.InNamespace(namespace)))
			want := 1
			if service != nil {
				want++
			}
			require.Len(t, services.Items, want, "only peer discovery and the requested client Service belong in recovery")
		})
	}
}
