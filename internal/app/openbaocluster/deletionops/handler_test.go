package deletionops

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

func TestHandleDefaultsRetentionSecrets(t *testing.T) {
	t.Parallel()

	cluster := newCleanupTestCluster("retain-defaults")
	cluster.UID = types.UID("retain-defaults-uid")
	cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyRetain

	ownerRef := metav1.OwnerReference{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoCluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
		Controller: ptrBool(true),
	}
	unsealSecret := newRetentionTestSecret(cluster, cluster.Name+constants.SuffixUnsealKey, ownerRef)
	rootTokenSecret := newRetentionTestSecret(cluster, cluster.Name+constants.SuffixRootToken, ownerRef)
	kubeClient := newCleanupTestClient(t, cluster, unsealSecret, rootTokenSecret)

	if err := Handle(context.Background(), logr.Discard(), Dependencies{Client: kubeClient}, cluster); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	for _, secretName := range []string{unsealSecret.Name, rootTokenSecret.Name} {
		secret := &corev1.Secret{}
		if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
			t.Fatalf("Get(%s) error = %v", secretName, err)
		}
		if len(secret.OwnerReferences) != 0 {
			t.Fatalf("secret %s ownerReferences = %#v, want empty", secretName, secret.OwnerReferences)
		}
	}
}

func newRetentionTestSecret(cluster *openbaov1alpha1.OpenBaoCluster, name string, ownerRef metav1.OwnerReference) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Data: map[string][]byte{"value": []byte("redacted")},
	}
}

func TestSourceDeletionWaitsForOwnedRestoreTest(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replaced), func(t *testing.T) {
			cluster := newCleanupTestCluster("source")
			cluster.UID = "source-uid"
			cluster.Status.Backup = &openbaov1alpha1.BackupStatus{RestoreTest: &openbaov1alpha1.RestoreTestStatus{
				Active: &openbaov1alpha1.RestoreTestRun{Namespace: "recovery", Name: "child", UID: "original"},
			}}
			child := &openbaov1alpha1.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{
				Name: "child", Namespace: "recovery", UID: "original",
				Annotations: map[string]string{constants.AnnotationRestoreTestSource: string(cluster.UID)},
				Finalizers:  []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
			}}
			if replaced {
				child.UID = "replacement"
			}
			c := newCleanupTestClient(t, cluster, child)
			err := Handle(t.Context(), logr.Discard(), Dependencies{Client: c}, cluster)
			if replaced {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(child), child))
			if replaced {
				require.Nil(t, child.DeletionTimestamp)
				return
			}
			require.NotNil(t, child.DeletionTimestamp)
			child.Finalizers = nil
			require.NoError(t, c.Update(t.Context(), child))
			require.NoError(t, Handle(t.Context(), logr.Discard(), Dependencies{Client: c}, cluster))
		})
	}
}

func TestSourceDeletionPreservesResourcesWhileRetentionExecutionIsUnknown(t *testing.T) {
	cluster := newCleanupTestCluster("retention-deletion")
	cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyDeleteAll
	stamp := metav1.Now()
	cluster.Status.Backup = &openbaov1alpha1.BackupStatus{Retention: &openbaov1alpha1.BackupRetentionStatus{
		Active: &openbaov1alpha1.BackupRetentionRun{Name: "absent-retention", SourceJobUID: "source-job",
			SubmittedAt: &stamp, DeadlineAt: stamp},
	}}
	pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-retained")
	// This path persists adminops-owned status before blocking cleanup.
	c := fake.NewClientBuilder().WithScheme(newCleanupTestClient(t).Scheme()).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).WithObjects(cluster, pvc).Build()
	// A stale deletion observation must still see the live retention reservation.
	stale := cluster.DeepCopy()
	stale.Status.Backup = nil
	require.ErrorContains(t, Handle(t.Context(), logr.Discard(), Dependencies{Client: c}, stale), "retention executor")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.NotNil(t, cluster.Status.Backup.Retention.Active)
}

type staleDeletionClient struct {
	client.Client
	snapshot     *openbaov1alpha1.OpenBaoCluster
	clusterReads int
}

func (c *staleDeletionClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if cluster, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok && key == client.ObjectKeyFromObject(c.snapshot) {
		c.clusterReads++
		*cluster = *c.snapshot.DeepCopy()
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestSourceDeletionUsesLiveReaderWhenClientCacheOmitsActiveRetention(t *testing.T) {
	cluster := newCleanupTestCluster("cached-retention-deletion")
	cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyDeleteAll
	stamp := metav1.Now()
	cluster.DeletionTimestamp = &stamp
	cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
	cluster.Status.Backup = &openbaov1alpha1.BackupStatus{Retention: &openbaov1alpha1.BackupRetentionStatus{
		Active: &openbaov1alpha1.BackupRetentionRun{Name: "absent-retention", SourceJobUID: "source-job",
			SubmittedAt: &stamp, DeadlineAt: stamp},
	}}
	pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-retained")
	live := fake.NewClientBuilder().WithScheme(newCleanupTestClient(t).Scheme()).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).WithObjects(cluster, pvc).Build()
	stale := cluster.DeepCopy()
	stale.Status.Backup = nil
	cached := &staleDeletionClient{Client: live, snapshot: stale.DeepCopy()}
	// The request object and all cached Client cluster reads omit the live reservation.
	require.ErrorContains(t, Handle(t.Context(), logr.Discard(), Dependencies{Client: cached, APIReader: live}, stale), "retention executor")
	require.Zero(t, cached.clusterReads, "custody and adminops read-back must use the live API reader")
	require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc))
	require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, []string{openbaov1alpha1.OpenBaoClusterFinalizer}, cluster.Finalizers)
	require.NotNil(t, cluster.Status.Backup.Retention.Active)
	require.Equal(t, "ExecutionUnknown", cluster.Status.Backup.Retention.Conditions[0].Reason)
}

func TestSourceDeletionUsesLiveRestoreTestAfterRetentionCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention bool
		bound     bool
	}{
		{name: "no retention, bound child", bound: true},
		{name: "no retention, unbound child"},
		{name: "unsubmitted retention, bound child", retention: true, bound: true},
		{name: "unsubmitted retention, unbound child", retention: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newCleanupTestCluster("cached-restore-test-deletion")
			cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyDeleteAll
			stamp := metav1.Now()
			cluster.DeletionTimestamp = &stamp
			cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
			run := &openbaov1alpha1.RestoreTestRun{Namespace: "recovery", Name: "child"}
			if tc.bound {
				run.UID = "child-uid"
			}
			cluster.Status.Backup = &openbaov1alpha1.BackupStatus{
				LastBackupName: "latest-snapshot",
				RestoreTest:    &openbaov1alpha1.RestoreTestStatus{Active: run},
			}
			if tc.retention {
				cluster.Status.Backup.Retention = &openbaov1alpha1.BackupRetentionStatus{
					Active: &openbaov1alpha1.BackupRetentionRun{Name: "unsubmitted-retention",
						SourceJobUID: "source-job", StartedAt: stamp, DeadlineAt: stamp},
				}
			}
			child := &openbaov1alpha1.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{
				Name: run.Name, Namespace: run.Namespace, UID: "child-uid",
				Annotations: map[string]string{constants.AnnotationRestoreTestSource: string(cluster.UID)},
				Finalizers:  []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
			}}
			pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-retained")
			desiredBackup := cluster.Status.Backup.DeepCopy()
			cluster.Status.Backup = nil
			live := fake.NewClientBuilder().WithScheme(newCleanupTestClient(t).Scheme()).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).WithObjects(cluster, child, pvc).Build()
			// Production reservations belong to the adminops SSA status manager.
			require.NoError(t, adminopsstatus.NewMutator(live, live)(t.Context(), cluster,
				func(current *openbaov1alpha1.OpenBaoCluster) error {
					current.Status.Backup = desiredBackup.DeepCopy()
					return nil
				}, adminops.ForceOwnership))
			stale := cluster.DeepCopy()
			stale.Status.Backup = nil
			cached := &staleDeletionClient{Client: live, snapshot: stale.DeepCopy()}
			deps := Dependencies{Client: cached, APIReader: live}

			require.ErrorContains(t, Handle(t.Context(), logr.Discard(), deps, stale),
				"waiting for disposable restore test cleanup")
			require.Zero(t, cached.clusterReads, "cancellation must use the live API reader")
			require.Nil(t, stale.Status.Backup, "cancellation must not replace the caller's cached status")
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(child), child))
			require.NotNil(t, child.DeletionTimestamp, "cross-namespace child deletion must start")
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc))
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, []string{openbaov1alpha1.OpenBaoClusterFinalizer}, cluster.Finalizers)
			require.NotNil(t, cluster.Status.Backup)
			require.Equal(t, "latest-snapshot", cluster.Status.Backup.LastBackupName)
			require.NotNil(t, cluster.Status.Backup.RestoreTest)
			require.NotNil(t, cluster.Status.Backup.RestoreTest.Active)
			require.Equal(t, child.UID, cluster.Status.Backup.RestoreTest.Active.UID)
			if tc.retention {
				require.NotNil(t, cluster.Status.Backup.Retention)
				require.Nil(t, cluster.Status.Backup.Retention.Active)
				require.NotNil(t, cluster.Status.Backup.Retention.Last)
				require.Equal(t, openbaov1alpha1.BackupRetentionSkipped, cluster.Status.Backup.Retention.Last.Outcome)
				require.Equal(t, "SourceDeleting", cluster.Status.Backup.Retention.Last.Reason)
			}

			child.Finalizers = nil
			require.NoError(t, live.Update(t.Context(), child))
			require.True(t, apierrors.IsNotFound(live.Get(t.Context(), client.ObjectKeyFromObject(child), child)))
			require.NoError(t, Handle(t.Context(), logr.Discard(), deps, stale))
			require.True(t, apierrors.IsNotFound(live.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc)),
				"PVC cleanup can complete after the recorded child disappears")
			require.Zero(t, cached.clusterReads)
		})
	}
}

func TestRestoreTestCancellationRejectsChangedSourceUIDBeforeStatusGuard(t *testing.T) {
	cluster := newCleanupTestCluster("replacement-source")
	live := newCleanupTestClient(t, cluster)
	stale := cluster.DeepCopy()
	stale.UID = "previous-source-uid"

	require.ErrorContains(t, backup.CancelRestoreTest(t.Context(), live, live, nil, stale),
		"source UID changed before restore test cancellation")
}
