//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/deletionops"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestVAP_RecoverySecretDeletion(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	operator := newPrivilegedImpersonatedClient(t, controllerUsername)
	const editorName = "recovery-secret-editor"
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "secret-editor", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"delete"}}}}
	require.NoError(t, k8sClient.Create(ctx, role))
	require.NoError(t, k8sClient.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: editorName}}}))
	editor := newImpersonatedClient(t, editorName)
	for _, suffix := range []string{constants.SuffixUnsealKey, constants.SuffixRootToken} {
		secret := recoverySecretFixture(namespace, "example", suffix)
		require.NoError(t, operator.Create(ctx, secret))
		for _, username := range []string{
			"system:kube-controller-manager", "system:controller:generic-garbage-collector",
			"system:serviceaccount:kube-system:generic-garbage-collector",
			"system:serviceaccount:kube-system:namespace-controller", "kubernetes-super-admin",
		} {
			t.Run(suffix+"/"+username, func(t *testing.T) {
				actor := newPrivilegedImpersonatedClient(t, username)
				require.Eventually(t, func() bool {
					return actor.Delete(ctx, secret.DeepCopy(), client.DryRunAll) != nil
				}, 5*time.Second, 50*time.Millisecond)
				require.ErrorContains(t, actor.Delete(ctx, secret.DeepCopy(), client.DryRunAll), "denied request: Generated recovery Secrets")
			})
		}
		// Existing maintenance authority still permits deliberate retained-data cleanup.
		secret.Annotations = map[string]string{constants.AnnotationMaintenance: "true"}
		require.NoError(t, operator.Update(ctx, secret))
		if suffix == constants.SuffixUnsealKey {
			require.ErrorContains(t, editor.Delete(ctx, secret.DeepCopy(), client.DryRunAll), "denied request: Generated recovery Secrets")
			grantClusterMaintenanceAccess(t, namespace, "example", editorName)
		}
		require.Eventually(t, func() bool { return editor.Delete(ctx, secret.DeepCopy(), client.DryRunAll) == nil }, 5*time.Second, 50*time.Millisecond)
		require.NoError(t, k8sClient.Delete(ctx, secret.DeepCopy(), client.DryRunAll))
		delete(secret.Annotations, constants.AnnotationMaintenance)
		require.NoError(t, operator.Update(ctx, secret))
		require.NoError(t, operator.Delete(ctx, secret.DeepCopy(), client.DryRunAll))
	}
	gc := newPrivilegedImpersonatedClient(t, "system:serviceaccount:kube-system:generic-garbage-collector")
	tls := recoverySecretFixture(namespace, "example", constants.SuffixTLSServer)
	require.NoError(t, operator.Create(ctx, tls))
	require.NoError(t, gc.Delete(ctx, tls, client.DryRunAll), "ordinary managed Secrets retain GC behavior")
	// Namespace termination still requires a caller allowed by the mutation lock.
	require.NoError(t, k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	secret := &corev1.Secret{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "example" + constants.SuffixUnsealKey}, secret))
	require.Eventually(t, func() bool { return gc.Delete(ctx, secret.DeepCopy(), client.DryRunAll) == nil }, 5*time.Second, 50*time.Millisecond)
	err := newPrivilegedImpersonatedClient(t, "other-user").Delete(ctx, secret.DeepCopy(), client.DryRunAll)
	require.ErrorContains(t, err, "denied request: Direct modification")
}

func TestRecoverySecretDeletionRealAPIRaces(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, scenario := range []string{"delete", "resource-version", "replacement", "retained-owner", "policy-retain", "restore-hold", "cluster-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := createMinimalCluster(t, namespace, "recovery")
			cluster.Spec.DeletionPolicy = api.DeletionPolicyDeletePVCs
			cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
			require.NoError(t, k8sClient.Update(ctx, cluster))
			operator := newControllerClient(t)
			secret := recoverySecretFixture(namespace, cluster.Name, constants.SuffixUnsealKey)
			secret.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)}
			if scenario == "retained-owner" {
				secret.Annotations[constants.AnnotationOpenBaoOwnerUID] = "previous-cluster"
			}
			require.NoError(t, operator.Create(ctx, secret))
			require.NoError(t, k8sClient.Delete(ctx, cluster))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			writer := interceptor.NewClient(operator, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					metadata, ok := obj.(*metav1.PartialObjectMetadata)
					require.True(t, ok, "cleanup must use Secret metadata only")
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.Equal(t, secret.UID, *options.Preconditions.UID)
					require.Equal(t, metadata.ResourceVersion, *options.Preconditions.ResourceVersion)
					if scenario == "resource-version" {
						secret.Annotations["test.openbao.org/race"] = "changed"
						require.NoError(t, c.Update(ctx, secret))
					}
					if scenario == "replacement" {
						require.NoError(t, c.Delete(ctx, secret))
						secret.UID, secret.ResourceVersion = "", ""
						require.NoError(t, c.Create(ctx, secret))
					}
					return c.Delete(ctx, obj, opts...)
				},
			})
			// Change authority after the workload check, at the metadata read.
			reader := interceptor.NewClient(operator, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*metav1.PartialObjectMetadata); ok {
						switch scenario {
						case "policy-retain":
							cluster.Spec.DeletionPolicy = api.DeletionPolicyRetain
							require.NoError(t, k8sClient.Update(ctx, cluster))
						case "restore-hold":
							cluster.Annotations = map[string]string{constants.AnnotationRestoreHold: "recovery"}
							require.NoError(t, k8sClient.Update(ctx, cluster))
						case "cluster-replacement":
							cluster.Finalizers = nil
							require.NoError(t, k8sClient.Update(ctx, cluster))
							createMinimalCluster(t, namespace, cluster.Name)
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			_, err := deletionops.Handle(ctx, logr.Discard(), deletionops.Dependencies{Client: writer, APIReader: reader}, cluster.DeepCopy())
			switch scenario {
			case "resource-version", "replacement":
				require.True(t, apierrors.IsConflict(err), "conditional delete must reject a race: %v", err)
			case "policy-retain", "restore-hold", "cluster-replacement":
				require.ErrorContains(t, err, "requires the original deleting cluster")
			default:
				require.NoError(t, err)
			}
			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{})
			if scenario == "delete" {
				require.True(t, apierrors.IsNotFound(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func recoverySecretFixture(namespace, cluster, suffix string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cluster + suffix, Namespace: namespace,
		Labels: map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			constants.LabelOpenBaoCluster: cluster}}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"key": []byte("test")}}
}
