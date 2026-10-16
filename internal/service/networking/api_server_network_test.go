package networking

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestEvaluateAPIServerNetworkReadiness(t *testing.T) {
	tests := []struct {
		name          string
		envHost       string
		cluster       *openbaov1alpha1.OpenBaoCluster
		wantStatus    metav1.ConditionStatus
		wantReason    string
		wantMessageIn string
	}{
		{
			name:          "service vip only is unknown hint",
			envHost:       "10.43.0.1",
			cluster:       &openbaov1alpha1.OpenBaoCluster{},
			wantStatus:    metav1.ConditionUnknown,
			wantReason:    "APIServerEndpointIPsRecommended",
			wantMessageIn: "post-DNAT enforcement",
		},
		{
			name:    "endpoint ips make readiness true",
			envHost: "10.43.0.1",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerEndpointIPs: []string{"192.168.166.2"},
					},
				},
			},
			wantStatus:    metav1.ConditionTrue,
			wantReason:    "APIServerNetworkReady",
			wantMessageIn: "192.168.166.2",
		},
		{
			name: "invalid cidr is false",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerCIDR: "not-a-cidr",
					},
				},
			},
			wantStatus:    metav1.ConditionFalse,
			wantReason:    "APIServerNetworkConfigurationInvalid",
			wantMessageIn: "spec.network.apiServerCIDR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost != "" {
				t.Setenv("KUBERNETES_SERVICE_HOST", tt.envHost)
			} else {
				t.Setenv("KUBERNETES_SERVICE_HOST", "")
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithReturnManagedFields().
				Build()
			manager := NewManagerWithReader(k8sClient, k8sClient, testScheme, "openbao-operator-system", "")

			readiness := manager.EvaluateAPIServerNetworkReadiness(context.Background(), logr.Discard(), tt.cluster)
			if readiness.Status != tt.wantStatus || readiness.Reason != tt.wantReason {
				t.Fatalf("readiness = %#v, want status=%s reason=%s", readiness, tt.wantStatus, tt.wantReason)
			}
			if tt.wantMessageIn != "" && !strings.Contains(readiness.Message, tt.wantMessageIn) {
				t.Fatalf("message = %q, want substring %q", readiness.Message, tt.wantMessageIn)
			}
		})
	}
}
