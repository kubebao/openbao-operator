//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	provisionerpkg "github.com/kubebao/openbao-operator/internal/service/provisioner"
)

func namespaceProvisionerClient(t *testing.T) client.WithWatch {
	t.Helper()
	config := rest.CopyConfig(cfg)
	config.Impersonate = rest.ImpersonationConfig{UserName: provisionerUsername, Groups: []string{"system:masters"}}
	c, err := client.NewWithWatch(config, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)
	return c
}

func namespaceTenantRBAC(namespace string) []client.Object {
	sa := provisionerpkg.OperatorServiceAccount{Name: "openbao-operator-controller", Namespace: "openbao-operator-system"}
	return []client.Object{
		provisionerpkg.GenerateTenantRole(namespace), provisionerpkg.GenerateTenantRoleBinding(namespace, sa),
		provisionerpkg.GenerateTenantSecretsReaderRole(namespace, []string{"user-credentials"}),
		provisionerpkg.GenerateTenantSecretsReaderRoleBinding(namespace, sa),
		provisionerpkg.GenerateTenantSecretsWriterRole(namespace, []string{"example-unseal-key"}),
		provisionerpkg.GenerateTenantSecretsWriterRoleBinding(namespace, sa),
	}
}

func TestVAP_TenantRBACGarbageCollectionHold(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	ensureProvisionerRBACApplied(t)
	namespace := newTestNamespace(t)
	provisioner := newPrivilegedImpersonatedClient(t, provisionerUsername)
	objects := namespaceTenantRBAC(namespace)
	for _, obj := range objects {
		require.NoError(t, provisioner.Create(ctx, obj))
	}
	kcm := newPrivilegedImpersonatedClient(t, "system:kube-controller-manager")
	require.Eventually(t, func() bool {
		err := kcm.Delete(ctx, objects[0], client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "Tenant RBAC must remain available")
	}, 5*time.Second, 50*time.Millisecond)
	for _, username := range []string{
		"system:kube-controller-manager", "system:serviceaccount:kube-system:namespace-controller",
		"system:controller:generic-garbage-collector",
	} {
		t.Run(username, func(t *testing.T) {
			gc := newPrivilegedImpersonatedClient(t, username)
			for _, obj := range objects {
				err := gc.Delete(ctx, obj, client.DryRunAll)
				requireAdmissionDenied(t, err)
				require.Contains(t, err.Error(), "Tenant RBAC must remain available")
				require.NoError(t, provisioner.Delete(ctx, obj, client.DryRunAll))
			}
		})
	}
	// Resources that no longer match the provisioner's ownership shape must
	// remain available to namespace cleanup; the provisioner will not delete them.
	updater := newPrivilegedImpersonatedClient(t, "system:serviceaccount:kube-system:generic-garbage-collector")
	for i, label := range []string{"app.kubernetes.io/name", "app.kubernetes.io/component"} {
		lookalike := objects[i].DeepCopyObject().(client.Object)
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(lookalike), lookalike))
		lookalike.GetLabels()[label] = "foreign"
		require.NoError(t, updater.Update(ctx, lookalike))
		require.NoError(t, kcm.Delete(ctx, lookalike, client.DryRunAll))
	}
	// The hold applies to provisioner-owned resources, not arbitrary namespace RBAC.
	unrelated := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: namespace}}
	require.NoError(t, k8sClient.Create(ctx, unrelated))
	require.NoError(t, kcm.Delete(ctx, unrelated, client.DryRunAll))
}

func TestProvisionerNamespaceFinalizationRealAPI(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	ensureProvisionerRBACApplied(t)
	for _, scenario := range []string{"pending-cluster", "role-version-race"} {
		t.Run(scenario, func(t *testing.T) {
			namespace := newTestNamespace(t)
			provisioner := namespaceProvisionerClient(t)
			objects := namespaceTenantRBAC(namespace)
			for _, obj := range objects {
				require.NoError(t, provisioner.Create(ctx, obj))
			}
			ns := &corev1.Namespace{}
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, ns))
			cluster := createMinimalCluster(t, namespace, "pending")
			cluster.Finalizers = []string{"test.example/hold"}
			require.NoError(t, k8sClient.Update(ctx, cluster))
			require.NoError(t, k8sClient.Delete(ctx, cluster))
			require.NoError(t, k8sClient.Delete(ctx, ns))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, ns))
			require.False(t, ns.DeletionTimestamp.IsZero())
			manager, err := provisionerpkg.NewManagerWithReader(provisioner, k8sClient, logr.Discard())
			require.NoError(t, err)
			done, err := manager.FinalizeNamespace(ctx, namespace, ns.UID)
			require.NoError(t, err)
			require.False(t, done)
			for _, obj := range objects {
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj))
			}
			// Release only this test's hold; no controller or foreign finalizer is stripped.
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			cluster.Finalizers = nil
			require.NoError(t, k8sClient.Update(ctx, cluster))
			if scenario == "role-version-race" {
				raced := false
				writer := interceptor.NewClient(provisioner, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
					require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
					if !raced {
						raced = true
						current := obj.DeepCopyObject().(client.Object)
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), current))
						current.SetAnnotations(map[string]string{"test.example/race": "changed"})
						require.NoError(t, c.Update(ctx, current))
					}
					return c.Delete(ctx, obj, opts...)
				}})
				racing, err := provisionerpkg.NewManagerWithReader(writer, k8sClient, logr.Discard())
				require.NoError(t, err)
				done, err = racing.FinalizeNamespace(ctx, namespace, ns.UID)
				require.Error(t, err)
				require.False(t, done)
				require.True(t, raced)
			}
			done, err = manager.FinalizeNamespace(ctx, namespace, ns.UID)
			require.NoError(t, err)
			require.True(t, done)
			for _, obj := range objects {
				require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)))
			}
		})
	}
}
