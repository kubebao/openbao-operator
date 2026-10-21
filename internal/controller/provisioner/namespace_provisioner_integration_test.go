//go:build integration
// +build integration

package provisioner_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	provisionersvc "github.com/kubebao/openbao-operator/internal/service/provisioner"
)

func TestNamespaceProvisioner_SetupWithManager_ProvisionsTenantNamespace(t *testing.T) {
	setAdmissionReady(t)

	ctx := context.Background()
	liveClient := startNamespaceProvisionerController(t)

	createNamespace(t, ctx, liveClient, operatorNamespace)
	createNamespace(t, ctx, liveClient, "tenant-provisioned")

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-provisioned",
			Namespace: operatorNamespace,
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: "tenant-provisioned",
		},
	}
	require.NoError(t, liveClient.Create(ctx, tenant))

	tenantKey := types.NamespacedName{Namespace: operatorNamespace, Name: tenant.Name}
	current := waitForTenantProvisioned(t, ctx, liveClient, tenantKey)
	condition := meta.FindStatusCondition(current.Status.Conditions, constants.TenantProvisionedConditionType)
	require.NotNil(t, condition)
	require.Equal(t, metav1.ConditionTrue, condition.Status)
	require.Equal(t, current.Generation, condition.ObservedGeneration)
	require.Equal(t, constants.ReasonTenantProvisioned, condition.Reason)
	require.Equal(t, "Tenant RBAC provisioned for namespace tenant-provisioned", condition.Message)
	require.False(t, condition.LastTransitionTime.IsZero())
	waitForRole(t, ctx, liveClient, types.NamespacedName{
		Namespace: "tenant-provisioned",
		Name:      provisionersvc.TenantRoleName,
	})
	waitForRoleBinding(t, ctx, liveClient, types.NamespacedName{
		Namespace: "tenant-provisioned",
		Name:      provisionersvc.TenantRoleBindingName,
	})
}

func TestNamespaceProvisioner_SetupWithManager_CleansUpTenantResourcesOnDelete(t *testing.T) {
	setAdmissionReady(t)

	ctx := context.Background()
	liveClient := startNamespaceProvisionerController(t)

	createNamespace(t, ctx, liveClient, operatorNamespace)
	createNamespace(t, ctx, liveClient, "tenant-cleanup")

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-cleanup",
			Namespace: operatorNamespace,
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: "tenant-cleanup",
		},
	}
	require.NoError(t, liveClient.Create(ctx, tenant))

	tenantKey := types.NamespacedName{Namespace: operatorNamespace, Name: tenant.Name}
	current := waitForTenantProvisioned(t, ctx, liveClient, tenantKey)
	quotaKey := types.NamespacedName{
		Namespace: "tenant-cleanup",
		Name:      provisionersvc.TenantResourceQuotaName,
	}
	limitRangeKey := types.NamespacedName{
		Namespace: "tenant-cleanup",
		Name:      provisionersvc.TenantLimitRangeName,
	}
	require.NoError(t, liveClient.Get(ctx, quotaKey, &corev1.ResourceQuota{}))
	require.NoError(t, liveClient.Get(ctx, limitRangeKey, &corev1.LimitRange{}))
	require.NoError(t, liveClient.Delete(ctx, current))

	waitForNotFound(t, ctx, liveClient, types.NamespacedName{
		Namespace: "tenant-cleanup",
		Name:      provisionersvc.TenantRoleName,
	}, provisionersvc.GenerateTenantRole("tenant-cleanup"))
	waitForNotFound(t, ctx, liveClient, types.NamespacedName{
		Namespace: "tenant-cleanup",
		Name:      provisionersvc.TenantRoleBindingName,
	}, provisionersvc.GenerateTenantRoleBinding("tenant-cleanup", provisionersvc.OperatorServiceAccount{
		Name:      "openbao-operator-controller",
		Namespace: operatorNamespace,
	}))
	waitForNotFound(t, ctx, liveClient, quotaKey, &corev1.ResourceQuota{})
	waitForNotFound(t, ctx, liveClient, limitRangeKey, &corev1.LimitRange{})
	waitForNotFound(t, ctx, liveClient, tenantKey, &openbaov1alpha1.OpenBaoTenant{})

	namespace := &corev1.Namespace{}
	require.NoError(t, liveClient.Get(ctx, types.NamespacedName{Name: "tenant-cleanup"}, namespace))
	for _, key := range []string{
		"pod-security.kubernetes.io/enforce",
		"pod-security.kubernetes.io/audit",
		"pod-security.kubernetes.io/warn",
	} {
		require.Equal(t, "restricted", namespace.Labels[key], "Pod Security label %s must remain after cleanup", key)
	}
}

func TestNamespaceProvisioner_SetupWithManager_PollsTerminatingNamespace(t *testing.T) {
	setAdmissionReady(t)
	ctx := context.Background()
	c := startNamespaceProvisionerController(t)
	createNamespace(t, ctx, c, operatorNamespace)
	const namespace = "tenant-terminating"
	createNamespace(t, ctx, c, namespace)
	tenant := &openbaov1alpha1.OpenBaoTenant{ObjectMeta: metav1.ObjectMeta{Name: namespace, Namespace: operatorNamespace}, Spec: openbaov1alpha1.OpenBaoTenantSpec{TargetNamespace: namespace}}
	require.NoError(t, c.Create(ctx, tenant))
	waitForTenantProvisioned(t, ctx, c, client.ObjectKeyFromObject(tenant))
	role := provisionersvc.GenerateTenantRole(namespace)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(role), role))
	originalUID := role.UID
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: namespace, Finalizers: []string{"test.example/hold"}}, Spec: openbaov1alpha1.OpenBaoClusterSpec{Profile: openbaov1alpha1.ProfileDevelopment, Version: "2.6.4", Replicas: 1, TLS: openbaov1alpha1.TLSConfig{Enabled: true, RotationPeriod: "720h"}, Storage: openbaov1alpha1.StorageConfig{Size: "1Gi"}}}
	require.NoError(t, c.Create(ctx, cluster))
	require.NoError(t, c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	// No tenant edit or Namespace watch triggers this check: the successful tenant
	// reconcile must schedule another GET of its declared target namespace.
	require.Eventually(t, func() bool {
		current := &openbaov1alpha1.OpenBaoTenant{}
		if c.Get(ctx, client.ObjectKeyFromObject(tenant), current) != nil {
			return false
		}
		return !current.Status.Provisioned && current.Status.LastError != ""
	}, 90*time.Second, 100*time.Millisecond)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(role), role))
	require.Equal(t, originalUID, role.UID)
	require.NoError(t, c.Delete(ctx, cluster))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.False(t, cluster.DeletionTimestamp.IsZero())
	cluster.Finalizers = nil
	require.NoError(t, c.Update(ctx, cluster))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(role), provisionersvc.GenerateTenantRole(namespace)))
	}, 20*time.Second, 100*time.Millisecond)
}
