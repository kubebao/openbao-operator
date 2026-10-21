package deletionops

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestDeletionPreservesRecoveryMaterialUntilWorkloadsAreGone(t *testing.T) {
	for _, policy := range []openbaov1alpha1.DeletionPolicy{
		openbaov1alpha1.DeletionPolicyRetain, openbaov1alpha1.DeletionPolicyDeletePVCs, openbaov1alpha1.DeletionPolicyDeleteAll,
	} {
		t.Run(string(policy), func(t *testing.T) {
			cluster := newCleanupTestCluster("workload-first")
			cluster.Spec.DeletionPolicy = policy
			set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, UID: "set-uid",
				Finalizers: []string{metav1.FinalizerDeleteDependents},
				Labels: map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
					constants.LabelOpenBaoCluster: cluster.Name},
				Annotations:     map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}}}
			secret := newRetentionTestSecret(cluster, cluster.Name+constants.SuffixUnsealKey,
				*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")))
			pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-workload-first-0")
			c := newCleanupTestClient(t, cluster, set, secret, pvc)
			result, err := Handle(t.Context(), logr.Discard(), Dependencies{Client: c, APIReader: c}, cluster)
			require.NoError(t, err)
			require.Positive(t, result.RequeueAfter)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
			require.True(t, HasClusterOwnerReference(secret, cluster), "retention material must stay protected until workload deletion completes")
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(set), set))
			require.NotNil(t, set.DeletionTimestamp)
			// Simulate completion of Kubernetes foreground garbage collection.
			set.Finalizers = nil
			require.NoError(t, c.Update(t.Context(), set))
			result, err = Handle(t.Context(), logr.Discard(), Dependencies{Client: c, APIReader: c}, cluster)
			require.NoError(t, err)
			require.Zero(t, result.RequeueAfter)
			err = c.Get(t.Context(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})
			if policy == openbaov1alpha1.DeletionPolicyRetain {
				require.NoError(t, err)
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
				require.False(t, HasClusterOwnerReference(secret, cluster))
			} else {
				require.True(t, apierrors.IsNotFound(err))
			}
		})
	}
}
