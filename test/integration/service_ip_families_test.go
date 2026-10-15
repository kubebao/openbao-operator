//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_ServiceIPFamilies(t *testing.T) {
	namespace := newTestNamespace(t)
	for _, pool := range []string{"main", "read"} {
		for index, tt := range []struct {
			name      string
			policy    corev1.IPFamilyPolicy
			families  []corev1.IPFamily
			wantError string
		}{
			{name: "legacy Service configuration"},
			{name: "IPv6 single stack", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{corev1.IPv6Protocol}},
			{name: "IPv6 primary dual stack", policy: corev1.IPFamilyPolicyRequireDualStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}},
			{name: "IPv4 primary dual stack", policy: corev1.IPFamilyPolicyPreferDualStack, families: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}},
			{name: "unknown policy", policy: "Unknown", wantError: "ipFamilyPolicy"},
			{name: "unknown family", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{"Unknown"}, wantError: "ipFamilies"},
			{name: "duplicate families", policy: corev1.IPFamilyPolicyRequireDualStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv6Protocol}, wantError: "duplicate"},
			{name: "single stack with two families", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}, wantError: "SingleStack"},
		} {
			t.Run(pool+"/"+tt.name, func(t *testing.T) {
				cluster := newMinimalClusterObj(namespace, fmt.Sprintf("families-%s-%d", pool, index))
				var policy *corev1.IPFamilyPolicy
				if tt.policy != "" {
					policy = ptr.To(tt.policy)
				}
				if pool == "main" {
					cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{IPFamilyPolicy: policy, IPFamilies: tt.families}
				} else {
					cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{Replicas: 1,
						Service: &openbaov1alpha1.ReadReplicaServiceConfig{Enabled: true, IPFamilyPolicy: policy, IPFamilies: tt.families},
					}
				}
				err := k8sClient.Create(ctx, cluster, client.DryRunAll)
				if tt.wantError != "" {
					requireInvalidRequest(t, err)
					require.ErrorContains(t, err, tt.wantError)
					return
				}
				require.NoError(t, err)
				if pool == "main" {
					require.Equal(t, tt.families, cluster.Spec.Service.IPFamilies)
					require.Equal(t, policy, cluster.Spec.Service.IPFamilyPolicy)
				} else {
					require.Equal(t, tt.families, cluster.Spec.ReadReplicas.Service.IPFamilies)
					require.Equal(t, policy, cluster.Spec.ReadReplicas.Service.IPFamilyPolicy)
				}
			})
		}
	}
}
