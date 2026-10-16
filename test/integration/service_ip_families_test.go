//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestKubernetesService_TwoFamiliesNeedDualStackPolicy(t *testing.T) {
	namespace := newTestNamespace(t)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-dual-policy", Namespace: namespace},
		Spec: corev1.ServiceSpec{
			IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
			Ports:      []corev1.ServicePort{{Port: 8200}},
		},
	}
	err := k8sClient.Create(ctx, service, client.DryRunAll)
	requireInvalidRequest(t, err)
	require.ErrorContains(t, err, "SingleStack")
}

func TestCRD_ServiceIPFamilies(t *testing.T) {
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	for _, pool := range []string{"main", "read", "restore", "scheduled"} {
		for index, tt := range []struct {
			name      string
			policy    corev1.IPFamilyPolicy
			families  []corev1.IPFamily
			wantError string
		}{
			{name: "legacy Service configuration"},
			{name: "one family with default policy", families: []corev1.IPFamily{corev1.IPv6Protocol}},
			{name: "IPv6 single stack", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{corev1.IPv6Protocol}},
			{name: "IPv6 primary dual stack", policy: corev1.IPFamilyPolicyRequireDualStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}},
			{name: "IPv4 primary dual stack", policy: corev1.IPFamilyPolicyPreferDualStack, families: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}},
			{name: "unknown policy", policy: "Unknown", wantError: "ipFamilyPolicy"},
			{name: "unknown family", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{"Unknown"}, wantError: "ipFamilies"},
			{name: "duplicate families", policy: corev1.IPFamilyPolicyRequireDualStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv6Protocol}, wantError: "duplicate"},
			{name: "single stack with two families", policy: corev1.IPFamilyPolicySingleStack, families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}, wantError: "Two IP families require"},
			{name: "IPv6 primary without dual-stack policy", families: []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}, wantError: "Two IP families require"},
			{name: "IPv4 primary without dual-stack policy", families: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}, wantError: "Two IP families require"},
		} {
			t.Run(pool+"/"+tt.name, func(t *testing.T) {
				cluster := newMinimalClusterObj(namespace, fmt.Sprintf("families-%s-%d", pool, index))
				var policy *corev1.IPFamilyPolicy
				if tt.policy != "" {
					policy = ptr.To(tt.policy)
				}
				var object client.Object = cluster
				var restored *openbaov1alpha1.RestoreTargetServiceConfig
				switch pool {
				case "main":
					cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{IPFamilyPolicy: policy, IPFamilies: tt.families}
				case "read":
					cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{Replicas: 1,
						Service: &openbaov1alpha1.ReadReplicaServiceConfig{Enabled: true, IPFamilyPolicy: policy, IPFamilies: tt.families},
					}
				case "restore", "scheduled":
					request := newFreshRestoreRequest(namespace, cluster.Name)
					restored = &openbaov1alpha1.RestoreTargetServiceConfig{IPFamilyPolicy: policy, IPFamilies: tt.families}
					request.Spec.ClusterTemplate.Service = restored
					if pool == "restore" {
						object = request
					} else {
						cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
							Schedule: "0 * * * *", JWTAuthRole: "backup", Target: request.Spec.Source.Target,
							RestoreTest: &openbaov1alpha1.RestoreTest{
								EverySuccessfulBackups: 1, Namespace: namespace,
								CredentialsSecretRef: &corev1.LocalObjectReference{Name: "reader"},
								ClusterTemplate:      *request.Spec.ClusterTemplate,
							},
						}
					}
				}
				err := k8sClient.Create(ctx, object, client.DryRunAll)
				if tt.wantError != "" {
					requireInvalidRequest(t, err)
					require.ErrorContains(t, err, tt.wantError)
					return
				}
				require.NoError(t, err)
				if pool == "main" {
					require.Equal(t, tt.families, cluster.Spec.Service.IPFamilies)
					require.Equal(t, policy, cluster.Spec.Service.IPFamilyPolicy)
				} else if pool == "read" {
					require.Equal(t, tt.families, cluster.Spec.ReadReplicas.Service.IPFamilies)
					require.Equal(t, policy, cluster.Spec.ReadReplicas.Service.IPFamilyPolicy)
				} else {
					if pool == "restore" {
						restored = object.(*openbaov1alpha1.OpenBaoRestore).Spec.ClusterTemplate.Service
					} else {
						restored = cluster.Spec.Backup.RestoreTest.ClusterTemplate.Service
					}
					require.Equal(t, tt.families, restored.IPFamilies)
					require.Equal(t, policy, restored.IPFamilyPolicy)
				}
			})
		}
	}
}
