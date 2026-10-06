package restore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
)

func TestRestoreCreationRecoversWithFreshManager(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		failStage    openbaov1alpha1.RestoreExecutionStage
		writeApplied bool
		wantStage    openbaov1alpha1.RestoreExecutionStage
		wantJobs     int
		wantUnknown  bool
	}{
		{
			name:      "commitment rejected before Job creation",
			failStage: openbaov1alpha1.RestoreExecutionStageCommitted,
			wantStage: openbaov1alpha1.RestoreExecutionStagePrepared,
		},
		{
			name:      "commitment acknowledgement lost before Job creation",
			failStage: openbaov1alpha1.RestoreExecutionStageCommitted, writeApplied: true,
			wantStage: openbaov1alpha1.RestoreExecutionStageCommitted, wantUnknown: true,
		},
		{
			name:      "Created receipt rejected after Job creation",
			failStage: openbaov1alpha1.RestoreExecutionStageCreated,
			wantStage: openbaov1alpha1.RestoreExecutionStageCommitted, wantJobs: 1,
		},
		{
			name:      "Created receipt acknowledgement lost after Job creation",
			failStage: openbaov1alpha1.RestoreExecutionStageCreated, writeApplied: true,
			wantStage: openbaov1alpha1.RestoreExecutionStageCreated, wantJobs: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRestoreRecoveryFixture(t)
			injected := errors.New("restore receipt write failed")
			failed := false
			f.client = interceptor.NewClient(f.base, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					r, ok := obj.(*openbaov1alpha1.OpenBaoRestore)
					if ok && !failed && r.Status.Execution.Stage == tt.failStage {
						failed = true
						if tt.writeApplied {
							require.NoError(t, c.Status().Patch(ctx, obj, patch, opts...))
						}
						return injected
					}
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			})

			require.ErrorIs(t, f.step(t), injected)
			require.True(t, failed)
			require.Equal(t, tt.wantStage, f.restore(t).Status.Execution.Stage)
			require.Equal(t, tt.wantJobs, f.jobCreates)
			f.requireLockHeld(t)
			require.NoError(t, f.step(t))
			if tt.wantUnknown {
				require.Equal(t, openbaov1alpha1.RestorePhaseUnknown, f.restore(t).Status.Phase)
				require.Equal(t, openbaov1alpha1.RestoreExecutionStageUnknown, f.restore(t).Status.Execution.Stage)
				require.NoError(t, f.step(t))
				require.Zero(t, f.jobCreates)
				f.requireLockHeld(t)
				return
			}
			f.requireCreatedIdentity(t)
			f.finishSuccessfulRestore(t)
		})
	}
}

func TestRestoreAmbiguousCreateRecoversWithFreshManager(t *testing.T) {
	t.Parallel()
	for _, lookupUnavailable := range []bool{false, true} {
		name := "Job observed immediately"
		if lookupUnavailable {
			name = "Job lookup also fails until the next manager"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newRestoreRecoveryFixture(t)
			created := false
			lookupFailed := false
			injected := apierrors.NewTimeoutError("response lost after server created Job", 1)
			f.client = interceptor.NewClient(f.base, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					require.NoError(t, c.Create(ctx, obj, opts...))
					if _, ok := obj.(*batchv1.Job); ok {
						created = true
						return injected
					}
					return nil
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*batchv1.Job); ok && created && lookupUnavailable && !lookupFailed {
						lookupFailed = true
						return injected
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			err := f.step(t)
			if lookupUnavailable {
				require.ErrorIs(t, err, injected)
				require.True(t, lookupFailed)
				require.Equal(t, openbaov1alpha1.RestoreExecutionStageCommitted, f.restore(t).Status.Execution.Stage)
			} else {
				require.NoError(t, err)
			}
			jobUID := f.job(t).UID
			require.NoError(t, f.step(t))
			f.requireCreatedIdentity(t)
			require.Equal(t, jobUID, f.restore(t).Status.Execution.JobUID)
			f.finishSuccessfulRestore(t)
		})
	}
}

func TestRestoreCreatedReceiptRecoveryRejectsDifferentOperation(t *testing.T) {
	t.Parallel()
	f := newRestoreRecoveryFixture(t)
	injected := errors.New("Created receipt rejected")
	f.client = interceptor.NewClient(f.base, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if r, ok := obj.(*openbaov1alpha1.OpenBaoRestore); ok && r.Status.Execution.Stage == openbaov1alpha1.RestoreExecutionStageCreated {
				return injected
			}
			return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
		},
	})
	require.ErrorIs(t, f.step(t), injected)
	require.Equal(t, openbaov1alpha1.RestoreExecutionStageCommitted, f.restore(t).Status.Execution.Stage)
	job := f.job(t)
	job.Annotations[restoreExecutionIDAnnotation] = "different-operation"
	require.NoError(t, f.base.Update(t.Context(), job))
	f.client = f.base
	for range 2 {
		require.NoError(t, f.step(t))
		require.Equal(t, openbaov1alpha1.RestorePhaseUnknown, f.restore(t).Status.Phase)
		require.Equal(t, openbaov1alpha1.RestoreExecutionStageUnknown, f.restore(t).Status.Execution.Stage)
		require.Empty(t, f.restore(t).Status.Execution.JobUID, "a mismatched Job must not receive a creation receipt")
		require.Equal(t, job.UID, f.job(t).UID)
		f.requireLockHeld(t)
	}
	require.Equal(t, 1, f.jobCreates)
}

func TestRestoreTerminalWritesRecoverWithFreshManager(t *testing.T) {
	t.Parallel()
	for _, succeeded := range []bool{false, true} {
		for _, terminalPhase := range []bool{false, true} {
			for _, writeApplied := range []bool{false, true} {
				name := fmt.Sprintf("succeeded=%t/terminalPhase=%t/writeApplied=%t", succeeded, terminalPhase, writeApplied)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					f := newRestoreRecoveryFixture(t)
					require.NoError(t, f.step(t))
					f.requireCreatedIdentity(t)
					f.markJobTerminal(t, succeeded)
					if succeeded && terminalPhase {
						// First request the restart and prove that an unsettled voter
						// workload retains the lock before allowing terminal completion.
						require.NoError(t, f.step(t))
						f.requireRestartRequested(t)
						f.setVotersReady(t)
					}
					injected := errors.New("terminal restore status response failed")
					failed := false
					f.client = interceptor.NewClient(f.base, interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							r, ok := obj.(*openbaov1alpha1.OpenBaoRestore)
							matches := ok && r.Status.Execution.Stage == openbaov1alpha1.RestoreExecutionStageTerminalObserved
							if ok && terminalPhase {
								matches = r.Status.Phase == openbaov1alpha1.RestorePhaseCompleted || r.Status.Phase == openbaov1alpha1.RestorePhaseFailed
							}
							if matches && !failed {
								failed = true
								if writeApplied {
									require.NoError(t, c.Status().Patch(ctx, obj, patch, opts...))
								}
								return injected
							}
							return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
						},
					})

					require.ErrorIs(t, f.step(t), injected)
					require.True(t, failed)
					f.requireLockHeld(t)
					stored := f.restore(t)
					if terminalPhase && writeApplied {
						require.NotNil(t, stored.Status.CompletionTime)
					} else {
						require.Equal(t, openbaov1alpha1.RestorePhaseRunning, stored.Status.Phase)
						require.Nil(t, stored.Status.CompletionTime)
					}
					if !terminalPhase && !writeApplied {
						require.Equal(t, openbaov1alpha1.RestoreExecutionStageCreated, stored.Status.Execution.Stage)
					}
					require.NoError(t, f.step(t))
					if succeeded && !terminalPhase {
						f.requireRestartRequested(t)
						f.setVotersReady(t)
						require.NoError(t, f.step(t))
					}
					f.requireTerminalCleanup(t, succeeded)
				})
			}
		}
	}
}

func TestRestoreFailedJobCleanupRecoversWithFreshManager(t *testing.T) {
	t.Parallel()
	f := newRestoreRecoveryFixture(t)
	require.NoError(t, f.step(t))
	f.requireCreatedIdentity(t)
	f.markJobTerminal(t, false)
	require.NoError(t, f.step(t))
	failedStatus := f.restore(t).Status
	require.Equal(t, openbaov1alpha1.RestorePhaseFailed, failedStatus.Phase)
	injected := errors.New("retained restore Job deletion failed")
	deleteFailed := false
	f.client = interceptor.NewClient(f.base, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*batchv1.Job); ok && !deleteFailed {
				deleteFailed = true
				return injected
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	require.ErrorIs(t, f.step(t), injected)
	require.True(t, deleteFailed)
	require.Equal(t, failedStatus, f.restore(t).Status)
	require.Equal(t, failedStatus.Execution.JobUID, f.job(t).UID)
	f.requireTerminalCleanup(t, false)
}

func TestRestoreRestartWritesRecoverWithFreshManager(t *testing.T) {
	t.Parallel()
	for _, completing := range []bool{false, true} {
		for _, readBackLost := range []bool{false, true} {
			name := fmt.Sprintf("completing=%t/readBackLost=%t", completing, readBackLost)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newRestoreRecoveryFixture(t)
				require.NoError(t, f.step(t))
				f.requireCreatedIdentity(t)
				f.markJobTerminal(t, true)
				if completing {
					require.NoError(t, f.step(t))
					f.requireRestartRequested(t)
					f.setVotersReady(t)
				}
				injected := errors.New("restart status persistence failed")
				failed := false
				failReadBack := false
				applyCalls := 0
				f.client = interceptor.NewClient(f.base, interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, c client.Client, subresource string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						applyCalls++
						if !failed {
							failed = true
							if !readBackLost {
								return injected
							}
							require.NoError(t, c.SubResource(subresource).Apply(ctx, obj, opts...))
							failReadBack = true
							return nil
						}
						return c.SubResource(subresource).Apply(ctx, obj, opts...)
					},
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok && failReadBack {
							failReadBack = false
							return injected
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})

				require.ErrorIs(t, f.step(t), injected)
				require.True(t, failed)
				f.requireLockHeld(t)
				require.Equal(t, openbaov1alpha1.RestorePhaseRunning, f.restore(t).Status.Phase)
				restart := f.cluster(t).Status.Restore
				if completing {
					require.NotNil(t, restart)
					require.Equal(t, readBackLost, restart.RestartCompletedAt != nil)
				} else if readBackLost {
					require.NotNil(t, restart)
					require.Equal(t, string(f.restore(t).UID), restart.UID)
				} else {
					require.Nil(t, restart)
				}
				require.NoError(t, f.step(t))
				if readBackLost {
					require.Equal(t, 1, applyCalls, "the durable restart receipt must be reused after lost read-back")
				} else {
					require.Equal(t, 2, applyCalls, "a rejected restart write must be retried")
				}
				if !completing {
					f.requireRestartRequested(t)
					f.setVotersReady(t)
					require.NoError(t, f.step(t))
				}
				f.requireTerminalCleanup(t, true)
			})
		}
	}
}

// These fixtures test recovery decisions across injected client failures. Child
// Job completion and StatefulSet convergence are supplied observations, not proof
// of API-server SSA semantics, controller watches, or real Pod restarts.
type restoreRecoveryFixture struct {
	scheme     *runtime.Scheme
	base       client.WithWatch
	client     client.WithWatch
	key        client.ObjectKey
	clusterKey client.ObjectKey
	jobCreates int
}

func newRestoreRecoveryFixture(t *testing.T) *restoreRecoveryFixture {
	t.Helper()
	f := &restoreRecoveryFixture{scheme: runtime.NewScheme()}
	require.NoError(t, openbaov1alpha1.AddToScheme(f.scheme))
	require.NoError(t, batchv1.AddToScheme(f.scheme))
	require.NoError(t, appsv1.AddToScheme(f.scheme))
	require.NoError(t, corev1.AddToScheme(f.scheme))
	r := newRunningRestoreForObservation()
	r.Finalizers = []string{openbaov1alpha1.OpenBaoRestoreFinalizer}
	r.Spec.Image = "example.com/restore:0.5.0"
	r.Spec.JWTAuthRole = "restore"
	r.Spec.Source = openbaov1alpha1.RestoreSource{
		Key:    "snapshots/restore.snap",
		Target: openbaov1alpha1.BackupTarget{Endpoint: "https://s3.example.com", Bucket: "snapshots"},
	}
	r.Status.Execution = newRestoreExecutionStatus(r)
	setTestResourceVersion(r)
	cluster := newRestoreObservationCluster(r)
	setTestResourceVersion(cluster)
	cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
	cluster.Spec.Replicas = 3
	cluster.Status.Initialized = true
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationRestore,
		Holder:    constants.ControllerNameOpenBaoRestore + "/" + r.Name,
	}
	sts := managedVoterStatefulSetForCluster(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Generation: 2},
		Spec:       appsv1.StatefulSetSpec{Replicas: &cluster.Spec.Replicas},
	}, cluster)
	f.key, f.clusterKey = client.ObjectKeyFromObject(r), client.ObjectKeyFromObject(cluster)
	f.base = fake.NewClientBuilder().WithScheme(f.scheme).WithObjects(r, cluster, sts).
		WithStatusSubresource(r, cluster, &batchv1.Job{}, sts).WithReturnManagedFields().
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if job, ok := obj.(*batchv1.Job); ok {
					f.jobCreates++
					// The fake client does not assign server UIDs. Give each create
					// attempt a distinct UID so identity assertions detect replay.
					job.UID = types.UID(fmt.Sprintf("restore-job-%d", f.jobCreates))
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	f.client = f.base
	return f
}

func (f *restoreRecoveryFixture) restore(t *testing.T) *openbaov1alpha1.OpenBaoRestore {
	t.Helper()
	r := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, f.base.Get(t.Context(), f.key, r))
	return r
}

func (f *restoreRecoveryFixture) cluster(t *testing.T) *openbaov1alpha1.OpenBaoCluster {
	t.Helper()
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, f.base.Get(t.Context(), f.clusterKey, cluster))
	return cluster
}

func (f *restoreRecoveryFixture) job(t *testing.T) *batchv1.Job {
	t.Helper()
	r := f.restore(t)
	job := &batchv1.Job{}
	require.NoError(t, f.base.Get(t.Context(), client.ObjectKey{Namespace: r.Namespace, Name: r.Status.Execution.JobName}, job))
	return job
}

func (f *restoreRecoveryFixture) step(t *testing.T) error {
	t.Helper()
	// Every call discards the previous Manager and all in-memory API objects.
	manager := withTestAdminOpsStatusPersistence(NewManager(f.client, f.scheme, nil, nil, ""), f.client)
	_, err := manager.Reconcile(t.Context(), testLogger(), f.restore(t))
	return err
}

func (f *restoreRecoveryFixture) requireLockHeld(t *testing.T) {
	t.Helper()
	r := f.restore(t)
	lock := f.cluster(t).Status.OperationLock
	require.NotNil(t, lock)
	require.Equal(t, openbaov1alpha1.ClusterOperationRestore, lock.Operation)
	require.Equal(t, constants.ControllerNameOpenBaoRestore+"/"+r.Name, lock.Holder)
}

func (f *restoreRecoveryFixture) requireCreatedIdentity(t *testing.T) {
	t.Helper()
	r, job := f.restore(t), f.job(t)
	require.Equal(t, openbaov1alpha1.RestoreExecutionStageCreated, r.Status.Execution.Stage)
	require.Equal(t, string(r.UID), r.Status.Execution.OperationID)
	require.Equal(t, r.Status.Execution.OperationID, job.Annotations[restoreExecutionIDAnnotation])
	require.Equal(t, types.UID("restore-job-1"), job.UID)
	require.Equal(t, job.UID, r.Status.Execution.JobUID)
	require.Equal(t, 1, f.jobCreates)
	f.requireLockHeld(t)
}

func (f *restoreRecoveryFixture) markJobTerminal(t *testing.T, succeeded bool) {
	t.Helper()
	job := f.job(t)
	if succeeded {
		job.Status.Succeeded = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	} else {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	}
	require.NoError(t, f.base.Status().Update(t.Context(), job))
}

func (f *restoreRecoveryFixture) requireRestartRequested(t *testing.T) {
	t.Helper()
	r := f.restore(t)
	require.Equal(t, openbaov1alpha1.RestorePhaseRunning, r.Status.Phase)
	restart := f.cluster(t).Status.Restore
	require.NotNil(t, restart)
	require.Equal(t, r.Name, restart.Name)
	require.Equal(t, string(r.UID), restart.UID)
	require.Nil(t, restart.RestartCompletedAt)
	require.Equal(t, r.Status.Execution.JobUID, f.job(t).UID)
	f.requireLockHeld(t)
}

func (f *restoreRecoveryFixture) setVotersReady(t *testing.T) {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	require.NoError(t, f.base.Get(t.Context(), f.clusterKey, sts))
	sts.Spec.Template.Annotations = map[string]string{constants.AnnotationRestoreRevision: string(f.restore(t).UID)}
	require.NoError(t, f.base.Update(t.Context(), sts))
	sts.Status = appsv1.StatefulSetStatus{
		ObservedGeneration: sts.Generation, Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3, CurrentReplicas: 3,
		CurrentRevision: "restored-revision", UpdateRevision: "restored-revision",
	}
	require.NoError(t, f.base.Status().Update(t.Context(), sts))
}

func (f *restoreRecoveryFixture) finishSuccessfulRestore(t *testing.T) {
	t.Helper()
	f.markJobTerminal(t, true)
	require.NoError(t, f.step(t))
	f.requireRestartRequested(t)
	f.setVotersReady(t)
	require.NoError(t, f.step(t))
	f.requireTerminalCleanup(t, true)
}

func (f *restoreRecoveryFixture) requireTerminalCleanup(t *testing.T, succeeded bool) {
	t.Helper()
	r := f.restore(t)
	wantPhase := openbaov1alpha1.RestorePhaseFailed
	wantResult := openbaov1alpha1.RestoreExecutionResultFailed
	if succeeded {
		wantPhase = openbaov1alpha1.RestorePhaseCompleted
		wantResult = openbaov1alpha1.RestoreExecutionResultSucceeded
		require.Equal(t, openbaov1alpha1.RestoreExecutionStageFollowThroughComplete, r.Status.Execution.Stage)
		restart := f.cluster(t).Status.Restore
		require.NotNil(t, restart)
		require.Equal(t, string(r.UID), restart.UID)
		require.NotNil(t, restart.RestartCompletedAt)
	}
	require.Equal(t, wantPhase, r.Status.Phase)
	require.Equal(t, wantResult, r.Status.Execution.TerminalResult)
	require.Equal(t, types.UID("restore-job-1"), r.Status.Execution.JobUID)
	require.NotNil(t, r.Status.CompletionTime)
	for range 2 {
		require.NoError(t, f.step(t))
		require.Equal(t, r.Status, f.restore(t).Status, "terminal receipts must remain unchanged")
	}
	require.Nil(t, f.cluster(t).Status.OperationLock)
	jobs := &batchv1.JobList{}
	require.NoError(t, f.base.List(t.Context(), jobs))
	require.Empty(t, jobs.Items)
	require.Equal(t, 1, f.jobCreates, "cleanup must not replay the restore")
}
