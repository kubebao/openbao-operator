package deletionops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	scenarioRestoreHold = "restore-hold"
	scenarioNotDeleting = "not-deleting"
	scenarioNoFinalizer = "no-finalizer"
)

func TestDeleteRecoverySecrets(t *testing.T) {
	for _, scenario := range []string{"delete-pvcs", "delete-all", "legacy-owner-reference", "foreign-owner", "missing-proof",
		"unmanaged", "wrong-cluster-label", "retain", scenarioNotDeleting, scenarioNoFinalizer, scenarioRestoreHold, "replacement-cluster"} {
		t.Run(scenario, func(t *testing.T) {
			cluster := newCleanupTestCluster("recovery")
			cluster.Spec.DeletionPolicy = api.DeletionPolicyDeletePVCs
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + constants.SuffixUnsealKey,
				Namespace: cluster.Namespace, UID: "secret-uid", Labels: map[string]string{
					constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator, constants.LabelOpenBaoCluster: cluster.Name},
				Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)}}, Data: map[string][]byte{"key": []byte("test")}}
			current := cluster.DeepCopy()
			switch scenario {
			case "delete-all":
				current.Spec.DeletionPolicy = api.DeletionPolicyDeleteAll
			case "legacy-owner-reference":
				secret.Annotations = nil
				secret.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(cluster, api.GroupVersion.WithKind("OpenBaoCluster"))}
			case "foreign-owner":
				secret.Annotations[constants.AnnotationOpenBaoOwnerUID] = "old-cluster"
			case "missing-proof":
				secret.Annotations = nil
			case "unmanaged":
				delete(secret.Labels, constants.LabelAppManagedBy)
			case "wrong-cluster-label":
				secret.Labels[constants.LabelOpenBaoCluster] = "other"
			case "retain":
				current.Spec.DeletionPolicy = api.DeletionPolicyRetain
			case scenarioNotDeleting:
				current.DeletionTimestamp = nil
			case scenarioNoFinalizer:
				current.Finalizers = []string{"test.openbao.org/hold"}
			case scenarioRestoreHold:
				current.Annotations = map[string]string{constants.AnnotationRestoreHold: "recovery"}
			case "replacement-cluster":
				current.UID = "new-cluster"
			}
			base := newCleanupTestClient(t, current, secret)
			deletes := 0
			c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					_, readsPayload := obj.(*corev1.Secret)
					require.False(t, readsPayload, "cleanup must not read Secret data")
					return c.Get(ctx, key, obj, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
					require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
					return c.Delete(ctx, obj, opts...)
				},
			})
			err := deleteRecoverySecrets(t.Context(), c, c, cluster)
			blocked := scenario == "retain" || scenario == scenarioNotDeleting || scenario == scenarioNoFinalizer || scenario == scenarioRestoreHold || scenario == "replacement-cluster"
			if blocked {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			deleted := scenario == "delete-pvcs" || scenario == "delete-all" || scenario == "legacy-owner-reference"
			require.Equal(t, map[bool]int{false: 0, true: 1}[deleted], deletes)
			require.Equal(t, deleted, apierrors.IsNotFound(base.Get(t.Context(), client.ObjectKeyFromObject(secret), &corev1.Secret{})))
		})
	}
}
