package networking

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func TestClientServiceFamilyValidationRequiresSuccessfulLiveRead(t *testing.T) {
	cluster := newMinimalCluster("family-reader", "default")
	cluster.Spec.Service = &api.ServiceConfig{IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol}}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster).WithReturnManagedFields().Build()
	failure := errors.New("service read unavailable")
	reader := fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return failure
		},
	}).Build()
	manager := NewManagerWithReader(c, reader, testScheme, "operators", constants.PlatformKubernetes)
	require.ErrorIs(t, manager.ensureExternalService(t.Context(), logr.Discard(), cluster), failure)
	err := c.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: externalServiceName(cluster)}, &corev1.Service{})
	require.True(t, apierrors.IsNotFound(err), "a failed live read must not allow an apply")
}

func TestClientServicesPreservePrimaryIPFamily(t *testing.T) {
	t.Parallel()
	for _, pool := range []struct {
		name   string
		ensure func(*Manager, context.Context, logr.Logger, *api.OpenBaoCluster) error
		key    func(*api.OpenBaoCluster) string
	}{
		{"main", (*Manager).ensureExternalService, externalServiceName},
		{"read", (*Manager).ensureReadReplicaService, resourceidentity.ReadReplicaServiceName},
	} {
		for _, tt := range []struct {
			name     string
			existing []corev1.IPFamily
			desired  []corev1.IPFamily
			reject   bool
		}{
			{"new Service", nil, []corev1.IPFamily{corev1.IPv6Protocol}, false},
			{"retain defaults", []corev1.IPFamily{corev1.IPv4Protocol}, nil, false},
			{"add secondary", []corev1.IPFamily{corev1.IPv4Protocol}, []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}, false},
			{"remove secondary", []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}, []corev1.IPFamily{corev1.IPv6Protocol}, false},
			{"change IPv4 primary", []corev1.IPFamily{corev1.IPv4Protocol}, []corev1.IPFamily{corev1.IPv6Protocol}, true},
			{"change IPv6 primary", []corev1.IPFamily{corev1.IPv6Protocol}, []corev1.IPFamily{corev1.IPv4Protocol}, true},
			{"swap dual-stack primary", []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}, []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}, true},
		} {
			t.Run(pool.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				cluster := newMinimalCluster("primary-family", "default")
				configure := func(families []corev1.IPFamily) {
					policy := ptr.To(corev1.IPFamilyPolicyPreferDualStack)
					cluster.Spec.Service = &api.ServiceConfig{IPFamilyPolicy: policy, IPFamilies: families}
					cluster.Spec.ReadReplicas = &api.ReadReplicaConfig{Replicas: 1,
						Service: &api.ReadReplicaServiceConfig{Enabled: true, IPFamilyPolicy: policy, IPFamilies: families}}
				}
				c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster).WithReturnManagedFields().Build()
				manager := NewManager(c, testScheme, "operators", constants.PlatformKubernetes)
				if tt.existing != nil {
					configure(tt.existing)
					require.NoError(t, pool.ensure(manager, t.Context(), logr.Discard(), cluster))
				}
				configure(tt.desired)
				err := pool.ensure(manager, t.Context(), logr.Discard(), cluster)
				if !tt.reject {
					require.NoError(t, err)
					return
				}
				require.ErrorContains(t, err, "primary IP family")
				service := &corev1.Service{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: pool.key(cluster)}, service))
				require.Equal(t, tt.existing, service.Spec.IPFamilies)
			})
		}
	}
}
