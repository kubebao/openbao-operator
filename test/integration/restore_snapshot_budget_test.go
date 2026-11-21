//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_RestoreSnapshotBudget(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	for _, tc := range []struct {
		name         string
		budget, size int64
		valid        bool
	}{
		{"default", 0, 123, true},
		{"default-too-small", 0, 9 * 1024 * 1024 * 1024, false},
		{"larger", 16 * 1024 * 1024 * 1024, 9 * 1024 * 1024 * 1024, true},
		{"larger-too-small", 16 * 1024 * 1024 * 1024, 16*1024*1024*1024 + 1, false},
		{"negative", -1, 123, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := newFreshRestoreRequest(namespace, tc.name)
			request.Spec.Source.MaxSnapshotSizeBytes, request.Spec.Source.ExpectedSize = tc.budget, tc.size
			err := k8sClient.Create(ctx, request)
			if !tc.valid {
				requireInvalidRequest(t, err)
				return
			}
			require.NoError(t, err)
			if tc.budget == 0 {
				require.Equal(t, int64(8*1024*1024*1024), request.Spec.Source.MaxSnapshotSizeBytes)
			}
			before := request.DeepCopy()
			request.Spec.Source.MaxSnapshotSizeBytes++
			require.Eventually(t, func() bool {
				return k8sClient.Patch(ctx, request, client.MergeFrom(before), client.DryRunAll) != nil
			}, 10*time.Second, 100*time.Millisecond, "restore budgets must remain immutable")
			request = before
			request.Status.SubmissionClaim = &api.RestoreSubmissionClaim{
				PodUID: "executor", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Size: tc.size, ClaimedAt: metav1.Now(), TargetPodName: "target-0", TargetPodUID: "target-pod",
				TargetPodIP: "10.1.2.3", TargetContainerID: "containerd://original",
			}
			oversized := request.DeepCopy()
			oversized.Status.SubmissionClaim.Size = request.Spec.Source.MaxSnapshotSizeBytes + 1
			require.Eventually(t, func() bool {
				return k8sClient.Status().Update(ctx, oversized, client.DryRunAll) != nil
			}, 10*time.Second, 100*time.Millisecond, "admission must reject claims above the request budget")
			require.NoError(t, k8sClient.Status().Update(ctx, request), "large observed sizes must remain representable")
		})
	}
	cluster := newMinimalClusterObj(namespace, "restore-test-budget")
	cluster.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 * * *", Target: api.BackupTarget{Endpoint: "https://storage.example", Bucket: "snapshots"},
		RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: namespace, ServiceAccountName: "reader",
			ClusterTemplate: *newFreshRestoreRequest(namespace, "template").Spec.ClusterTemplate},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	require.Equal(t, int64(8*1024*1024*1024), cluster.Spec.Backup.RestoreTest.MaxSnapshotSizeBytes)
	before := cluster.DeepCopy()
	cluster.Spec.Backup.RestoreTest.MaxSnapshotSizeBytes = 16 * 1024 * 1024 * 1024
	require.NoError(t, k8sClient.Patch(ctx, cluster, client.MergeFrom(before)))
	cluster.Status.Backup = &api.BackupStatus{LatestSnapshot: &api.BackupSnapshotSummary{Size: 9 * 1024 * 1024 * 1024,
		ClusterID: "source", Version: "2.7.0", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	require.NoError(t, k8sClient.Status().Update(ctx, cluster))
}
