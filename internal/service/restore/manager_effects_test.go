package restore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
)

func TestApplyRestoreDecision_CommittedRecordsCreatedReceiptOnly(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenBao API to scheme: %v", err)
	}

	restore := newRunningRestoreForObservation()
	setTestResourceVersion(restore)
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	restore.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCommitted
	job := managedRestoreJobForRestore(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      restore.Status.Execution.JobName,
		Namespace: restore.Namespace,
		UID:       types.UID("committed-job-uid"),
	}}, restore)
	job.Annotations[restoreExecutionIDAnnotation] = restore.Status.Execution.OperationID
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()
	manager := &Manager{client: k8sClient}
	observation := restoreObservation{
		job: job,
		state: restoreState{
			executionStage: openbaov1alpha1.RestoreExecutionStageCommitted,
			jobState:       restoreJobSucceeded,
		},
	}
	decision := decideRestore(observation.state)

	result, err := manager.applyRestoreDecision(context.Background(), testLogger(), restore, observation, decision)
	if err != nil {
		t.Fatalf("applyRestoreDecision() error = %v", err)
	}
	if result.RequeueAfter != restoreRequeueImmediately {
		t.Fatalf("requeue = %v, want %v", result.RequeueAfter, restoreRequeueImmediately)
	}

	updated := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updated); err != nil {
		t.Fatalf("get updated restore: %v", err)
	}
	if updated.Status.Execution.Stage != openbaov1alpha1.RestoreExecutionStageCreated {
		t.Fatalf("execution stage = %q, want %q", updated.Status.Execution.Stage, openbaov1alpha1.RestoreExecutionStageCreated)
	}
	if updated.Status.Execution.TerminalResult != "" {
		t.Fatalf("terminal result = %q, want empty until the next observation", updated.Status.Execution.TerminalResult)
	}
}

func TestHandleRunning_TerminalReceiptFailureOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		succeeded        bool
		failReceiptWrite bool
		wantStage        openbaov1alpha1.RestoreExecutionStage
		wantResult       openbaov1alpha1.RestoreExecutionResult
		wantClusterReads int
	}{
		{
			name: "success receipt precedes recovery lock renewal", succeeded: true,
			wantStage:  openbaov1alpha1.RestoreExecutionStageTerminalObserved,
			wantResult: openbaov1alpha1.RestoreExecutionResultSucceeded, wantClusterReads: 2,
		},
		{
			name:      "failed job renews lock before recording failure",
			wantStage: openbaov1alpha1.RestoreExecutionStageCreated, wantClusterReads: 2,
		},
		{
			name: "success receipt write failure stops recovery", succeeded: true, failReceiptWrite: true,
			wantStage: openbaov1alpha1.RestoreExecutionStageCreated, wantClusterReads: 1,
		},
		{
			name: "failure receipt write failure stops terminal transition", failReceiptWrite: true,
			wantStage: openbaov1alpha1.RestoreExecutionStageCreated, wantClusterReads: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			require.NoError(t, batchv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			restore := newRunningRestoreForObservation()
			setTestResourceVersion(restore)
			restore.Status.Execution = newRestoreExecutionStatus(restore)
			restore.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCreated
			cluster := newRestoreObservationCluster(restore)
			setTestResourceVersion(cluster)
			job := managedRestoreJobForRestore(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: restore.Status.Execution.JobName, Namespace: restore.Namespace, UID: "terminal-job-uid",
			}}, restore)
			restore.Status.Execution.JobUID = job.UID
			if tt.succeeded {
				job.Status.Succeeded = 1
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			} else {
				job.Status.Failed = 1
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
			}

			injectedErr := errors.New("injected persistence failure")
			clusterReads := 0
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, restore, job).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
				WithReturnManagedFields().
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok {
							clusterReads++
							if clusterReads > 1 && !tt.failReceiptWrite {
								return injectedErr
							}
						}
						return c.Get(ctx, key, obj, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, ok := obj.(*openbaov1alpha1.OpenBaoRestore); ok && tt.failReceiptWrite {
							return injectedErr
						}
						return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			manager := &Manager{client: k8sClient, reader: k8sClient, scheme: scheme}

			_, err := manager.handleRunning(t.Context(), testLogger(), restore)
			require.ErrorIs(t, err, injectedErr)
			require.Equal(t, tt.wantClusterReads, clusterReads)
			updated := &openbaov1alpha1.OpenBaoRestore{}
			require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(restore), updated))
			require.Equal(t, tt.wantStage, updated.Status.Execution.Stage)
			require.Equal(t, tt.wantResult, updated.Status.Execution.TerminalResult)
			require.Equal(t, openbaov1alpha1.RestorePhaseRunning, updated.Status.Phase)
			require.Nil(t, updated.Status.CompletionTime)
		})
	}
}

func TestHandleRunning_LegacyAdoptionRechecksJobIdentity(t *testing.T) {
	t.Parallel()

	for _, missing := range []bool{false, true} {
		name := "replaced job"
		if missing {
			name = "missing job"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			require.NoError(t, batchv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			restore := newRunningRestoreForObservation()
			setTestResourceVersion(restore)
			cluster := newRestoreObservationCluster(restore)
			job := managedRestoreJobForRestore(&batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: restoreJobName(restore), Namespace: restore.Namespace, UID: "legacy-job-uid"},
				Status:     batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
			}, restore)
			jobReads := 0
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, restore, job).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
				WithReturnManagedFields().
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if observedJob, ok := obj.(*batchv1.Job); ok {
							jobReads++
							if jobReads == 2 {
								persisted := &openbaov1alpha1.OpenBaoRestore{}
								require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(restore), persisted))
								require.NotNil(t, persisted.Status.Execution)
								require.Equal(t, openbaov1alpha1.RestoreExecutionStageCreated, persisted.Status.Execution.Stage)
								if missing {
									return apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, key.Name)
								}
								observedJob.UID = "replacement-job-uid"
							}
						}
						return nil
					},
				}).Build()
			manager := &Manager{client: k8sClient, reader: k8sClient, scheme: scheme}

			result, err := manager.handleRunning(t.Context(), testLogger(), restore)
			require.NoError(t, err)
			require.Zero(t, result.RequeueAfter)
			require.Equal(t, 2, jobReads)
			updated := &openbaov1alpha1.OpenBaoRestore{}
			require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(restore), updated))
			require.Equal(t, openbaov1alpha1.RestorePhaseUnknown, updated.Status.Phase)
			require.Equal(t, openbaov1alpha1.RestoreExecutionStageUnknown, updated.Status.Execution.Stage)
			require.Equal(t, job.UID, updated.Status.Execution.JobUID)
			require.Empty(t, updated.Status.Execution.TerminalResult)
		})
	}
}

func TestHandleRunning_LegacyAdoptionContinuesObservation(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenBao API to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch API to scheme: %v", err)
	}

	restore := newRunningRestoreForObservation()
	setTestResourceVersion(restore)
	cluster := newRestoreObservationCluster(restore)
	setTestResourceVersion(cluster)
	lock := restoreOperationLock(restore)
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: lock.Operation,
		Holder:    lock.Holder,
		Message:   restoreLockMessage(restore),
	}
	job := managedRestoreJobForRestore(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      restoreJobName(restore),
		Namespace: restore.Namespace,
		UID:       types.UID("legacy-running-job-uid"),
	}}, restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()
	manager := &Manager{
		client: k8sClient,
		reader: k8sClient,
		scheme: scheme,
	}

	result, err := manager.handleRunning(context.Background(), testLogger(), restore)
	if err != nil {
		t.Fatalf("handleRunning() error = %v", err)
	}
	if result.RequeueAfter != restoreRequeueJobPoll {
		t.Fatalf("requeue = %v, want %v", result.RequeueAfter, restoreRequeueJobPoll)
	}

	updated := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updated); err != nil {
		t.Fatalf("get updated restore: %v", err)
	}
	if updated.Status.Execution == nil {
		t.Fatal("execution receipt is nil after legacy adoption")
	}
	if updated.Status.Execution.Stage != openbaov1alpha1.RestoreExecutionStageCreated {
		t.Fatalf("execution stage = %q, want %q", updated.Status.Execution.Stage, openbaov1alpha1.RestoreExecutionStageCreated)
	}
	if updated.Status.Message != restoreJobRunningStatusMessage(job.Name) {
		t.Fatalf("status message = %q, want %q", updated.Status.Message, restoreJobRunningStatusMessage(job.Name))
	}
}
