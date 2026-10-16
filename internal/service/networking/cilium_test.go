package networking

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
)

func ciliumTestCluster() *api.OpenBaoCluster {
	return &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "tenant", UID: "cluster-uid"},
		Spec: api.OpenBaoClusterSpec{Network: &api.NetworkConfig{APIServerEgress: &api.APIServerEgressConfig{Mode: "Cilium"}}}}
}

func TestCiliumAPIServerPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		port    int32
		mode    string
		want    string
		invalid bool
	}{
		{name: "default", mode: "Cilium", want: "6443"}, {name: "cloud", mode: "Cilium", port: 443, want: "443"},
		{name: "custom", mode: "Cilium", port: 7443, want: "7443"}, {name: "invalid mode", mode: "Other", invalid: true},
		{name: "negative port", mode: "Cilium", port: -1, invalid: true}, {name: "large port", mode: "Cilium", port: 65536, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := ciliumTestCluster()
			cluster.Spec.Network.APIServerEgress.Port = tc.port
			cluster.Spec.Network.APIServerEgress.Mode = tc.mode
			obj, err := buildCiliumAPIServerPolicy(cluster)
			if tc.invalid {
				require.ErrorIs(t, err, ErrAPIServerNetworkConfigurationInvalid)
				require.ErrorIs(t, err, operatorerrors.ErrPermanentConfig)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "tenant", obj.GetNamespace())
			require.Equal(t, "bao-api-server", obj.GetName())
			require.Equal(t, map[string]interface{}{
				"endpointSelector":  map[string]interface{}{"matchLabels": map[string]interface{}{constants.LabelOpenBaoCluster: "bao"}},
				"enableDefaultDeny": map[string]interface{}{"ingress": false, "egress": false},
				"egress":            []interface{}{map[string]interface{}{"toEntities": []interface{}{"kube-apiserver"}, "toPorts": []interface{}{map[string]interface{}{"ports": []interface{}{map[string]interface{}{"port": tc.want, "protocol": "TCP"}}}}}},
			}, obj.Object["spec"])
		})
	}
}

func TestCiliumAPIServerReadiness(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00::1")
	for _, tc := range []struct {
		name    string
		current string
		want    metav1.ConditionStatus
		reason  string
	}{
		{"pending", "absent", metav1.ConditionUnknown, constants.ReasonAPIServerEgressPolicyPending},
		{"ready", "owned", metav1.ConditionTrue, constants.ReasonAPIServerNetworkReady},
		{"drift", "drift", metav1.ConditionUnknown, constants.ReasonAPIServerEgressPolicyPending},
		{"collision", "unowned", metav1.ConditionFalse, constants.ReasonAPIServerNetworkConfigurationInvalid},
		{"restore", "restore", metav1.ConditionUnknown, constants.ReasonAPIServerNetworkManagedExternally},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := ciliumTestCluster()
			builder := fake.NewClientBuilder().WithScheme(testScheme)
			if tc.current != "absent" && tc.current != "restore" {
				obj, err := buildCiliumAPIServerPolicy(cluster)
				require.NoError(t, err)
				if tc.current != "unowned" {
					require.NoError(t, resourceapply.PrepareOwned(obj, cluster, testScheme))
				}
				if tc.current == "drift" {
					obj.Object["spec"] = map[string]interface{}{}
				}
				builder.WithObjects(obj)
			}
			if tc.current == "restore" {
				cluster.Annotations = map[string]string{constants.AnnotationRestoreOrigin: "request"}
			}
			c := builder.Build()
			m := NewManager(c, testScheme, "operator", "")
			got := m.EvaluateAPIServerNetworkReadiness(context.Background(), logr.Discard(), cluster)
			require.Equal(t, tc.want, got.Status)
			require.Equal(t, tc.reason, got.Reason)
			if got.Status == metav1.ConditionTrue {
				require.Contains(t, got.Message, "not live API connectivity")
			}
		})
	}
}

func TestCiliumAPIServerUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"missing API", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "cilium.io", Kind: "CiliumNetworkPolicy"}}},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: "cilium.io", Resource: "ciliumnetworkpolicies"}, "bao-api-server", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(interceptor.Funcs{Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Group == "cilium.io" {
					return tc.err
				}
				return nil
			}}).Build()
			m := NewManager(c, testScheme, "operator", "")
			cluster := ciliumTestCluster()
			err := m.ensureCiliumAPIServerPolicy(context.Background(), logr.Discard(), cluster)
			require.ErrorIs(t, err, ErrAPIServerNetworkConfigurationInvalid)
			require.ErrorIs(t, err, operatorerrors.ErrPermanentConfig)
			got := m.evaluateCiliumAPIServerPolicy(context.Background(), cluster)
			require.Equal(t, metav1.ConditionFalse, got.Status)
		})
	}
}

func TestCiliumAPIServerDisablePreservesUnownedPolicy(t *testing.T) {
	cluster := ciliumTestCluster()
	obj, err := buildCiliumAPIServerPolicy(cluster)
	require.NoError(t, err)
	cluster.Spec.Network.APIServerEgress = nil
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(obj).Build()
	m := NewManager(c, testScheme, "operator", "")
	require.Error(t, m.ensureCiliumAPIServerPolicy(context.Background(), logr.Discard(), cluster))
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(obj.GroupVersionKind())
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(obj), current))
}

func TestCiliumAPIServerDisableRetriesForbiddenCleanup(t *testing.T) {
	for _, verb := range []string{"get", "delete"} {
		t.Run(verb, func(t *testing.T) {
			cluster := ciliumTestCluster()
			obj, err := buildCiliumAPIServerPolicy(cluster)
			require.NoError(t, err)
			require.NoError(t, resourceapply.PrepareOwned(obj, cluster, testScheme))
			cluster.Spec.Network.APIServerEgress = nil
			blocked := true
			reads := 0
			forbidden := apierrors.NewForbidden(schema.GroupResource{
				Group: "cilium.io", Resource: "ciliumnetworkpolicies",
			}, obj.GetName(), nil)
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(obj).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						reads++
						if blocked && verb == "get" {
							return forbidden
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if blocked && verb == "delete" {
							return forbidden
						}
						return c.Delete(ctx, obj, opts...)
					},
				}).Build()
			m := NewManager(c, testScheme, "operator", "")
			err = m.ensureCiliumAPIServerPolicy(context.Background(), logr.Discard(), cluster)
			require.True(t, apierrors.IsForbidden(err), "%v", err)
			require.NotErrorIs(t, err, operatorerrors.ErrPermanentConfig)
			shouldRequeue, _ := operatorerrors.ShouldRequeue(err)
			require.True(t, shouldRequeue)
			require.Positive(t, reads, "cleanup must read the policy before deciding whether deletion is safe")
			current := newCiliumAPIServerPolicy(cluster)
			blocked = false
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(obj), current))
			require.NoError(t, m.ensureCiliumAPIServerPolicy(context.Background(), logr.Discard(), cluster))
			require.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(obj), current)))
		})
	}
}
