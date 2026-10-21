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
			secret.UID = "secret-uid"
			secret.Labels = map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster: cluster.Name}
			pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-workload-first-0")
			c := newCleanupTestClient(t, cluster, set, secret, pvc)
			result, err := Handle(t.Context(), logr.Discard(), Dependencies{Client: c, APIReader: c}, cluster)
			require.NoError(t, err)
			require.Positive(t, result.RequeueAfter)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
			require.Equal(t, policy != openbaov1alpha1.DeletionPolicyRetain, HasClusterOwnerReference(secret, cluster),
				"Retain must orphan recovery material before waiting for workload deletion")
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
				require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(secret), &corev1.Secret{})))
			}
		})
	}
}

func TestRetainOrphaningRequiresCurrentDeletionAuthority(t *testing.T) {
	for _, scenario := range []string{scenarioRestoreHold, "changed-cluster-uid", scenarioNotDeleting, scenarioNoFinalizer} {
		t.Run(scenario, func(t *testing.T) {
			cluster := newCleanupTestCluster("retain-authority")
			cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyRetain
			secret := newRetentionTestSecret(cluster, cluster.Name+constants.SuffixUnsealKey,
				*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")))
			current := cluster.DeepCopy()
			switch scenario {
			case scenarioRestoreHold:
				current.Annotations = map[string]string{constants.AnnotationRestoreHold: "recovery"}
			case "changed-cluster-uid":
				current.UID = "changed-cluster-uid"
			case scenarioNotDeleting:
				current.DeletionTimestamp = nil
			case scenarioNoFinalizer:
				current.Finalizers = []string{"test.openbao.org/hold"}
			}
			c := newCleanupTestClient(t, current, secret)
			_, err := Handle(t.Context(), logr.Discard(), Dependencies{Client: c, APIReader: c}, cluster)
			require.Error(t, err)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
			require.True(t, HasClusterOwnerReference(secret, cluster))
		})
	}
}
