package networking

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func TestServicesExcludeHelperPodsAndPreserveWorkloadPools(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		ensure       func(*Manager, context.Context, logr.Logger, *api.OpenBaoCluster) error
		serviceName  func(*api.OpenBaoCluster) string
		selectVoters bool
	}{
		{"headless", (*Manager).ensureHeadlessService, resourceidentity.HeadlessServiceName, true},
		{"public", (*Manager).ensureExternalService, externalServiceName, true},
		{"read replicas", (*Manager).ensureReadReplicaService, resourceidentity.ReadReplicaServiceName, false},
		{"all-node metrics", (*Manager).ensureMetricsService, metricsServiceName, true},
		{"ACME", (*Manager).ensureACMEChallengeService, acmeServiceName, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cluster := newMinimalCluster("backend-test", "default")
			cluster.Spec.Service = &api.ServiceConfig{}
			cluster.Spec.ReadReplicas = &api.ReadReplicaConfig{Replicas: 1,
				Service: &api.ReadReplicaServiceConfig{Enabled: true}}
			cluster.Spec.Observability = &api.ObservabilityConfig{
				Metrics: &api.MetricsConfig{Enabled: true, ScrapeProfile: metricsScrapeProfileAllNode}}
			cluster.Spec.TLS.Mode = api.TLSModeACME
			cluster.Spec.TLS.ACME = &api.ACMEConfig{
				DirectoryURL: "https://acme.example.test/directory", Domains: []string{"bao.example.test"},
			}
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster).WithReturnManagedFields().Build()
			m := NewManager(c, testScheme, "operators", constants.PlatformKubernetes)
			require.NoError(t, tt.ensure(m, t.Context(), logr.Discard(), cluster))
			svc := &corev1.Service{}
			require.NoError(t, c.Get(t.Context(),
				client.ObjectKey{Namespace: cluster.Namespace, Name: tt.serviceName(cluster)}, svc))
			selector := labels.SelectorFromSet(svc.Spec.Selector)
			for _, pool := range []string{
				constants.LabelValueOpenBaoWorkloadPoolVoter, constants.LabelValueOpenBaoWorkloadPoolReadReplica,
			} {
				server := resourceidentity.PodSelectorLabelsForPool(cluster, pool)
				server[constants.LabelOpenBaoComponent] = constants.ComponentOpenBaoCluster
				want := tt.selectVoters || pool == constants.LabelValueOpenBaoWorkloadPoolReadReplica
				require.Equal(t, want, selector.Matches(labels.Set(server)), "server pool %s", pool)
			}
			for _, component := range []string{"upgrade", "backup", "restore", "upgrade-snapshot", "autopilot"} {
				helper := resourceidentity.Labels(cluster)
				helper[constants.LabelOpenBaoComponent] = component
				require.False(t, selector.Matches(labels.Set(helper)), "helper component %s", component)
			}
		})
	}
}
