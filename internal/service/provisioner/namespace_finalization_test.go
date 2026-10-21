package provisioner

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const namespaceFinalizationRoleUID = "role-uid"

func terminatingTenantNamespace() *corev1.Namespace {
	now := metav1.Now()
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace, UID: "namespace-uid", DeletionTimestamp: &now, Finalizers: []string{"test.example/hold"}}}
}

func TestFinalizeNamespacePreservesPendingWork(t *testing.T) {
	for _, kind := range []string{"cluster", "restore"} {
		t.Run(kind, func(t *testing.T) {
			ns := terminatingTenantNamespace()
			role := GenerateTenantRole(testNamespace)
			role.UID = namespaceFinalizationRoleUID
			var pending client.Object = &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: testNamespace}}
			if kind == "restore" {
				pending = &openbaov1alpha1.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: testNamespace}}
			}
			live := newTestClient(t, ns, role, pending)
			// The write client's empty cached lists must not release live permissions.
			cached := interceptor.NewClient(live.(client.WithWatch), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return nil }})
			manager, err := NewManagerWithReader(cached, live, logr.Discard())
			require.NoError(t, err)
			done, err := manager.FinalizeNamespace(t.Context(), testNamespace, ns.UID)
			require.NoError(t, err)
			require.False(t, done)
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(role), &rbacv1.Role{}))
		})
	}
}

func TestFinalizeNamespaceDeletesOnlyOwnedResources(t *testing.T) {
	ns := terminatingTenantNamespace()
	role := GenerateTenantRole(testNamespace)
	role.UID = namespaceFinalizationRoleUID
	binding := GenerateTenantRoleBinding(testNamespace, OperatorServiceAccount{Name: "controller", Namespace: "operator"})
	binding.UID = "binding-uid"
	foreign := GenerateTenantSecretsReaderRole(testNamespace, []string{"foreign"})
	foreign.Labels["app.kubernetes.io/name"] = "foreign"
	foreign.UID = "foreign-uid"
	unrelated := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: testNamespace, Labels: provisionerManagedLabels()}}
	c := newTestClient(t, ns, role, binding, foreign, unrelated)
	deletes := 0
	checked := interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{Delete: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		options := &client.DeleteOptions{}
		options.ApplyOptions(opts)
		require.NotNil(t, options.Preconditions)
		require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
		require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
		deletes++
		return inner.Delete(ctx, obj, opts...)
	}})
	manager, err := NewManagerWithReader(checked, c, logr.Discard())
	require.NoError(t, err)
	done, err := manager.FinalizeNamespace(t.Context(), testNamespace, ns.UID)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, 2, deletes)
	require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(role), &rbacv1.Role{})))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(foreign), &rbacv1.Role{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(unrelated), &rbacv1.Role{}))
}

func TestFinalizeNamespaceFailsClosed(t *testing.T) {
	for _, test := range []string{"active", "replaced", "cluster-list-error", "restore-list-error", "namespace-read-error", "namespace-replacement-race"} {
		t.Run(test, func(t *testing.T) {
			ns := terminatingTenantNamespace()
			expectedUID := ns.UID
			if test == "active" {
				ns.DeletionTimestamp = nil
			}
			if test == "replaced" {
				expectedUID = "previous-uid"
			}
			role := GenerateTenantRole(testNamespace)
			role.UID = namespaceFinalizationRoleUID
			c := newTestClient(t, ns, role)
			reads := 0
			reader := interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					err := inner.Get(ctx, key, obj, opts...)
					if n, ok := obj.(*corev1.Namespace); ok {
						reads++
						if test == "namespace-read-error" {
							return errors.New("namespace unavailable")
						}
						if test == "namespace-replacement-race" && reads > 1 {
							n.UID = "replacement-uid"
						}
					}
					return err
				},
				List: func(ctx context.Context, inner client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					_, clusters := list.(*openbaov1alpha1.OpenBaoClusterList)
					_, restores := list.(*openbaov1alpha1.OpenBaoRestoreList)
					if (test == "cluster-list-error" && clusters) || (test == "restore-list-error" && restores) {
						return errors.New("list unavailable")
					}
					return inner.List(ctx, list, opts...)
				},
			})
			manager, err := NewManagerWithReader(c, reader, logr.Discard())
			require.NoError(t, err)
			done, err := manager.FinalizeNamespace(t.Context(), testNamespace, expectedUID)
			require.Error(t, err)
			require.False(t, done)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(role), &rbacv1.Role{}))
		})
	}
}

func TestEnsureTenantSecretRBACPreservesTerminatingNamespace(t *testing.T) {
	ns := terminatingTenantNamespace()
	role := GenerateTenantSecretsWriterRole(testNamespace, []string{"pending-unseal-key"})
	c := newTestClient(t, ns, role)
	manager, err := NewManager(c, logr.Discard())
	require.NoError(t, err)
	require.NoError(t, manager.EnsureTenantSecretRBAC(t.Context(), testNamespace))
	current := &rbacv1.Role{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(role), current))
	require.Equal(t, role.Rules, current.Rules)
}
