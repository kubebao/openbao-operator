//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/restore"
)

func TestRestoreAcceptedAuthoritySurvivesPermissionRevocation(t *testing.T) {
	installRestoreExecutionPolicy(t)
	request, controller := newAcceptedRestoreAuthorityRequest(t)
	user := newImpersonatedClient(t, "restore-submitter")
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: "cluster-restore-access-binding", Namespace: request.Namespace,
	}}
	require.NoError(t, k8sClient.Delete(ctx, binding))

	// Prove that revocation is effective at admission before progressing the
	// already accepted request with the controller identity.
	probe := request.DeepCopy()
	probe.Name = "after-revocation"
	probe.UID, probe.ResourceVersion = "", ""
	require.Eventually(t, func() bool {
		err := user.Create(ctx, probe, client.DryRunAll)
		return apierrors.IsInvalid(err) && strings.Contains(err.Error(), "authorized to restore")
	}, 10*time.Second, 100*time.Millisecond)

	manager := restore.NewManager(controller, k8sScheme, nil, nil, "")
	for range 3 {
		require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
		_, err := manager.Reconcile(ctx, logr.Discard(), request)
		require.NoError(t, err)
	}
	require.Equal(t, api.RestoreExecutionStageCreated, request.Status.Execution.Stage)
	require.Equal(t, string(request.UID), request.Status.Execution.OperationID)
	job := &batchv1.Job{}
	require.NoError(t, controller.Get(ctx, client.ObjectKey{
		Namespace: request.Namespace, Name: request.Status.Execution.JobName,
	}, job))
	require.Equal(t, job.UID, request.Status.Execution.JobUID)
}

func TestRestoreCancellationAndCommitmentOrdering(t *testing.T) {
	installRestoreExecutionPolicy(t)
	for _, deletionWins := range []bool{true, false} {
		name := "commitment wins"
		if deletionWins {
			name = "deletion wins"
		}
		t.Run(name, func(t *testing.T) {
			request, controller := newAcceptedRestoreAuthorityRequest(t)
			manager := restore.NewManager(controller, k8sScheme, nil, nil, "")
			for range 2 {
				require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
				_, err := manager.Reconcile(ctx, logr.Discard(), request)
				require.NoError(t, err)
			}
			require.Equal(t, api.RestoreExecutionStagePrepared, request.Status.Execution.Stage)
			user := newImpersonatedClient(t, "restore-submitter")
			if deletionWins {
				deleted := false
				racing := interceptor.NewClient(controller, interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subresource string,
						obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption,
					) error {
						candidate, ok := obj.(*api.OpenBaoRestore)
						if ok && candidate.Status.Execution != nil &&
							candidate.Status.Execution.Stage == api.RestoreExecutionStageCommitted {
							require.NoError(t, user.Delete(ctx, request))
							deleted = true
						}
						return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
					},
				})
				_, err := restore.NewManager(racing, k8sScheme, nil, nil, "").Reconcile(ctx, logr.Discard(), request)
				require.True(t, deleted)
				require.True(t, apierrors.IsConflict(err), "stale commitment must conflict: %v", err)
				require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
				require.NotNil(t, request.DeletionTimestamp)
				require.Equal(t, api.RestoreExecutionStagePrepared, request.Status.Execution.Stage)
				jobs := &batchv1.JobList{}
				require.NoError(t, controller.List(ctx, jobs, client.InNamespace(request.Namespace)))
				require.Empty(t, jobs.Items)
				_, err = manager.Reconcile(ctx, logr.Discard(), request)
				require.NoError(t, err)
				require.True(t, apierrors.IsNotFound(controller.Get(ctx, client.ObjectKeyFromObject(request), request)))
				return
			}

			_, err := manager.Reconcile(ctx, logr.Discard(), request)
			require.NoError(t, err)
			require.Equal(t, api.RestoreExecutionStageCreated, request.Status.Execution.Stage)
			// Policy and binding watches are asynchronous. Probe with dry-run so
			// the first real deletion cannot race initial admission activation.
			require.Eventually(t, func() bool {
				err := user.Delete(ctx, request, client.DryRunAll)
				return err != nil && strings.Contains(err.Error(), "committed restore")
			}, 10*time.Second, 100*time.Millisecond)
			err = user.Delete(ctx, request)
			requireAdmissionDenied(t, err)
			require.Contains(t, err.Error(), "committed restore")
		})
	}
}

func TestRestoreNamedTargetIsPinnedDuringPreparation(t *testing.T) {
	request, controller := newAcceptedRestoreAuthorityRequest(t)
	original := &api.OpenBaoCluster{}
	key := client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}
	require.NoError(t, controller.Get(ctx, key, original))
	require.NoError(t, controller.Delete(ctx, original))
	replacement := createMinimalCluster(t, request.Namespace, request.Spec.Cluster)
	updateClusterStatus(t, replacement, func(status *api.OpenBaoClusterStatus) { status.Initialized = true })
	require.NotEqual(t, original.UID, replacement.UID)

	manager := restore.NewManager(controller, k8sScheme, nil, nil, "")
	for range 2 {
		require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
		_, err := manager.Reconcile(ctx, logr.Discard(), request)
		require.NoError(t, err)
	}
	require.Equal(t, api.RestoreExecutionStagePrepared, request.Status.Execution.Stage)
	require.Equal(t, replacement.UID, request.Status.Execution.TargetUID,
		"admission accepts named intent; preparation binds the then-current target")
}

func TestRestoreDisposableDeletionAfterCommitmentStartsCleanup(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	request := newFreshRestoreRequest(namespace, "disposable-deletion")
	request.Spec.TargetLifecycle = api.RestoreTargetLifecycleDisposable
	request.Finalizers = []string{api.OpenBaoRestoreFinalizer}
	require.NoError(t, k8sClient.Create(ctx, request))
	request.Status = api.OpenBaoRestoreStatus{
		Phase:  api.RestorePhaseRunning,
		Target: &api.RestoreTargetStatus{UID: "original-target", ReservedAt: metav1.Now()},
		Execution: &api.RestoreExecutionStatus{
			OperationID: string(request.UID), TargetUID: "original-target",
			JobName: restore.RestoreJobNamePrefix + request.Name, Stage: api.RestoreExecutionStageCommitted,
		},
	}
	controller := newControllerClient(t)
	require.NoError(t, controller.Status().Update(ctx, request))
	require.NoError(t, k8sClient.Delete(ctx, request))
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.NotNil(t, request.DeletionTimestamp)
	require.Contains(t, request.Finalizers, api.OpenBaoRestoreFinalizer)

	_, err := restore.NewManager(controller, k8sScheme, nil, nil, "").Reconcile(ctx, logr.Discard(), request)
	require.NoError(t, err)
	require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.Equal(t, api.RestoreTargetCleanupPending, request.Status.Target.Cleanup)
	require.Equal(t, api.RestoreExecutionStageCommitted, request.Status.Execution.Stage,
		"post-commit deletion starts cleanup without erasing execution uncertainty")
	require.Contains(t, request.Finalizers, api.OpenBaoRestoreFinalizer)
}

func newAcceptedRestoreAuthorityRequest(t *testing.T) (*api.OpenBaoRestore, client.WithWatch) {
	t.Helper()
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)
	cluster := createMinimalCluster(t, namespace, "authority-target")
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) { status.Initialized = true })
	const username = "restore-submitter"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterRestoreAccess(t, namespace, cluster.Name, username)
	grantClusterHelperImageAccess(t, namespace, cluster.Name, username)
	request := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "accepted", Namespace: namespace},
		Spec: api.OpenBaoRestoreSpec{
			Cluster: cluster.Name, JWTAuthRole: "restore-role", Image: "openbao-backup:dev",
			Source: api.RestoreSource{Key: "snapshot.snap", Target: api.BackupTarget{
				Bucket: testBackupBucket, Endpoint: "https://storage.example.com",
			}},
		},
	}
	require.NoError(t, newImpersonatedClient(t, username).Create(ctx, request))
	return request, newControllerClient(t)
}
