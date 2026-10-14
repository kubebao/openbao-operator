package opslifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestActiveRetentionBlocksFreshOperationAcquisitionAfterLockCleared(t *testing.T) {
	for _, operation := range []api.ClusterOperation{api.ClusterOperationBackup, api.ClusterOperationUpgrade, api.ClusterOperationRestore} {
		t.Run(string(operation), func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, api.AddToScheme(scheme))
			live := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "test", UID: "original"}}
			stale := live.DeepCopy()
			live.Status.Backup = &api.BackupStatus{Retention: &api.BackupRetentionStatus{
				Active: &api.BackupRetentionRun{Name: "retention"},
			}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.OpenBaoCluster{}).WithObjects(live).Build()
			err := AcquireWithReader(t.Context(), c, c, stale, OperationLock{Holder: "new-operation", Operation: operation}, AcquireOptions{Force: true})
			require.True(t, IsLockHeld(err), "%v", err)
			require.Nil(t, stale.Status.OperationLock)
		})
	}
}

func TestOperationLockMutationRejectsSameNameClusterReplacement(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(map[bool]string{false: "acquire", true: "release"}[release], func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, api.AddToScheme(scheme))
			live := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "test", UID: "replacement"}}
			lock := OperationLock{Holder: "backup", Operation: api.ClusterOperationBackup}
			live.Status.OperationLock = &api.OperationLockStatus{Holder: lock.Holder, Operation: lock.Operation}
			stale := live.DeepCopy()
			stale.UID = "original"
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.OpenBaoCluster{}).WithObjects(live).Build()
			var err error
			if release {
				err = ReleaseWithReader(t.Context(), c, c, stale, lock)
			} else {
				err = AcquireWithReader(t.Context(), c, c, stale, lock, AcquireOptions{})
			}
			require.ErrorContains(t, err, "UID changed")
		})
	}
}
