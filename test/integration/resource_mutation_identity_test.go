//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/deletionops"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
)

func TestOwnedResourceWriteRejectsReplacement(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, retained := range []bool{false, true} {
		for _, exists := range []bool{false, true} {
			for _, sameOwner := range []bool{false, true} {
				t.Run(fmt.Sprintf("retained=%t/existing=%t/sameOwner=%t", retained, exists, sameOwner), func(t *testing.T) {
					namespace := newTestNamespace(t)
					controller := newMutationController(t, namespace)
					owner := mutationOwner(namespace)
					original := mutationConfigMap(namespace, "race", string(owner.UID))
					if !retained {
						require.NoError(t, resourceapply.PrepareOwned(original, owner, k8sScheme))
					}
					if exists {
						require.NoError(t, controller.Create(ctx, original))
					}
					ownerUID := "replacement-owner"
					if sameOwner {
						ownerUID = string(owner.UID)
					}
					replacement := mutationConfigMap(namespace, original.Name, ownerUID)
					replacement.Data["value"] = "replacement"
					replace := func() {
						if exists {
							require.NoError(t, controller.Delete(ctx, original, client.Preconditions{UID: &original.UID}))
						}
						require.NoError(t, controller.Create(ctx, replacement))
					}
					writes := 0
					wrapped := interceptor.NewClient(controller, interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							writes++
							require.False(t, exists, "existing resources must use a conditional apply")
							replace()
							return c.Create(ctx, obj, opts...)
						},
						Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
							writes++
							require.True(t, exists, "absent resources must use create-only writes")
							replace()
							return c.Apply(ctx, obj, opts...)
						},
					})
					desired := mutationConfigMap(namespace, original.Name, string(owner.UID))
					desired.Data["value"] = "desired"
					var err error
					if retained {
						err = resourceapply.ApplyRetained(ctx, wrapped, owner, desired)
					} else {
						err = resourceapply.ApplyOwned(ctx, wrapped, k8sScheme, owner, desired)
					}
					require.Error(t, err)
					require.True(t, operatorerrors.IsTransient(err), "replacement must trigger a fresh observation: %v", err)
					require.Equal(t, 1, writes)
					current := &corev1.ConfigMap{}
					require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(replacement), current))
					require.Equal(t, replacement.UID, current.UID)
					require.Equal(t, replacement.Annotations, current.Annotations)
					require.Equal(t, replacement.Data, current.Data)
					require.Empty(t, current.OwnerReferences)
				})
			}
		}
	}
}

func TestOwnedResourceWriteRetriesAfterConcurrentUpdate(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	controller := newMutationController(t, namespace)
	owner := mutationOwner(namespace)
	original := mutationConfigMap(namespace, "race", string(owner.UID))
	require.NoError(t, controller.Create(ctx, original))
	wrapped := interceptor.NewClient(controller, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			original.Data["value"] = "concurrent"
			require.NoError(t, controller.Update(ctx, original))
			return c.Apply(ctx, obj, opts...)
		},
	})
	desired := mutationConfigMap(namespace, original.Name, string(owner.UID))
	desired.Data["value"] = "desired"
	err := resourceapply.ApplyOwned(ctx, wrapped, k8sScheme, owner, desired)
	require.True(t, apierrors.IsConflict(err), "stale resource version must conflict: %v", err)
	require.NoError(t, resourceapply.ApplyOwned(ctx, controller, k8sScheme, owner, desired))
	current := &corev1.ConfigMap{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(original), current))
	require.Equal(t, original.UID, current.UID)
	require.Equal(t, "desired", current.Data["value"])
}

func TestClusterCleanupPreservesReplacementPVC(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	controller := newMutationController(t, namespace)
	owner := mutationOwner(namespace)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: mutationConfigMap(namespace, "data-race-0", string(owner.UID)).ObjectMeta,
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	require.NoError(t, controller.Create(ctx, pvc))
	replacement := &corev1.PersistentVolumeClaim{ObjectMeta: mutationConfigMap(namespace, pvc.Name, "replacement-owner").ObjectMeta, Spec: pvc.Spec}
	wrapped := interceptor.NewClient(controller, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			require.NoError(t, controller.Delete(ctx, pvc, client.Preconditions{UID: &pvc.UID}))
			// EnvTest has no PVC-protection controller to finish this deletion.
			pending := &corev1.PersistentVolumeClaim{}
			if err := controller.Get(ctx, client.ObjectKeyFromObject(pvc), pending); err == nil {
				before := pending.DeepCopy()
				pending.Finalizers = nil
				require.NoError(t, controller.Patch(ctx, pending, client.MergeFrom(before)))
			} else {
				require.True(t, apierrors.IsNotFound(err), "read deleted claim: %v", err)
			}
			require.NoError(t, controller.Create(ctx, replacement))
			return c.Delete(ctx, obj, opts...)
		},
	})
	err := deletionops.Cleanup(ctx, logr.Discard(), wrapped, owner, api.DeletionPolicyDeletePVCs)
	require.True(t, apierrors.IsConflict(err), "replacement must reject the stale delete: %v", err)
	current := &corev1.PersistentVolumeClaim{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(replacement), current))
	require.Equal(t, replacement.UID, current.UID)
	require.Nil(t, current.DeletionTimestamp)
	require.Equal(t, replacement.Annotations, current.Annotations)
}

func mutationOwner(namespace string) *api.OpenBaoCluster {
	return &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "race", Namespace: namespace, UID: "original-owner"}}
}

func mutationConfigMap(namespace, name, ownerUID string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{
				constants.LabelOpenBaoCluster: "race",
				constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
			},
			Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: ownerUID},
		},
		Data: map[string]string{"value": "original"},
	}
}

// The controller receives only namespaced resource permissions. In particular,
// it has neither system:masters membership nor the maintenance verb.
func newMutationController(t *testing.T, namespace string) client.WithWatch {
	t.Helper()
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "mutation-test", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps", "persistentvolumeclaims"},
			Verbs: []string{"get", "list", "create", "update", "patch", "delete"},
		}},
	}
	require.NoError(t, k8sClient.Create(ctx, role))
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: controllerUsername},
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "mutation-test-user"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, binding))
	config := rest.CopyConfig(cfg)
	config.Impersonate = rest.ImpersonationConfig{UserName: controllerUsername}
	controller, err := client.NewWithWatch(config, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)
	probe := mutationConfigMap(namespace, "admission-probe", "probe-owner")
	require.NoError(t, controller.Create(ctx, probe))
	untrusted := newImpersonatedClient(t, "mutation-test-user")
	require.Eventually(t, func() bool {
		err := untrusted.Delete(ctx, probe, client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited")
	}, 5*time.Second, 50*time.Millisecond, "managed-resource admission must be active before probing the race")
	return controller
}

func TestOwnedResourceRecoversInterruptedCreateOwnership(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	controller := newMutationController(t, namespace)
	owner := mutationOwner(namespace)
	desired := mutationConfigMap(namespace, "created", string(owner.UID))
	desired.Data["obsolete"] = "remove after creation"
	wrapped := interceptor.NewClient(controller, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, obj.GetName(), fmt.Errorf("interrupted after create"))
		},
	})
	err := resourceapply.ApplyOwned(ctx, wrapped, k8sScheme, owner, desired.DeepCopy())
	require.True(t, apierrors.IsConflict(err), "injected ownership promotion interruption: %v", err)
	// A fresh reconcile must finish promotion before applying a changed desired state.
	delete(desired.Data, "obsolete")
	require.NoError(t, resourceapply.ApplyOwned(ctx, controller, k8sScheme, owner, desired))
	current := &corev1.ConfigMap{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(desired), current))
	require.Equal(t, desired.Data, current.Data)
}

func TestCreateOwnershipPromotionRejectsReplacement(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	controller := newMutationController(t, namespace)
	owner := mutationOwner(namespace)
	desired := mutationConfigMap(namespace, "created", string(owner.UID))
	replacement := mutationConfigMap(namespace, desired.Name, "replacement-owner")
	wrapped := interceptor.NewClient(controller, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			uid := obj.GetUID()
			require.NoError(t, controller.Delete(ctx, obj, client.Preconditions{UID: &uid}))
			require.NoError(t, controller.Create(ctx, replacement))
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	err := resourceapply.ApplyOwned(ctx, wrapped, k8sScheme, owner, desired)
	require.Error(t, err)
	// Kubernetes can reject either the stale resource version or the immutable UID.
	current := &corev1.ConfigMap{}
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(replacement), current))
	require.Equal(t, replacement.UID, current.UID)
	require.Equal(t, replacement.Annotations, current.Annotations)
	require.Equal(t, replacement.Data, current.Data)
	require.Empty(t, current.OwnerReferences)
}
