//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/adapter/security"
	"github.com/dc-tec/openbao-operator/internal/service/restore"
)

// Supply Job-controller and kubelet observations through a real API server.
// This test checks lifecycle decisions, not physical executor termination.
func TestRestoreManager_WaitsForTerminalJobAndExecutorObservations(t *testing.T) {
	for _, succeeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("succeeded=%t", succeeded), func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := createRestoreModelCluster(t, namespace)
			r := createRestoreModelRequest(t, namespace, cluster.Name, restoreLifecycleScenario{
				Path: restoreLifecycleSuccess, NameSuffix: "executor",
			})
			controllerClient := newControllerClient(t)
			step := func() {
				t.Helper()
				// Discard the manager and its objects on each reconcile.
				mgr := withIntegrationRestoreStatusPersistence(restore.NewManager(
					controllerClient, k8sScheme, nil,
					security.NewImageVerifier(logr.Discard(), k8sClient, nil), "",
				).WithReader(k8sClient), controllerClient)
				latest := getRestoreModelRequest(t, namespace, r.Name)
				_, err := mgr.Reconcile(ctx, logr.Discard(), latest)
				require.NoError(t, err)
			}
			for range 3 {
				step()
			}
			job := &batchv1.Job{}
			key := client.ObjectKey{Namespace: namespace, Name: restore.RestoreJobNamePrefix + r.Name}
			require.NoError(t, k8sClient.Get(ctx, key, job))
			originalJobUID := job.UID
			assertWaiting := func() {
				t.Helper()
				for range 2 {
					step()
					latest := getRestoreModelRequest(t, namespace, r.Name)
					require.Equal(t, openbaov1alpha1.RestorePhaseRunning, latest.Status.Phase)
					require.Equal(t, openbaov1alpha1.RestoreExecutionStageCreated, latest.Status.Execution.Stage)
					require.Equal(t, originalJobUID, latest.Status.Execution.JobUID)
					require.Empty(t, latest.Status.Execution.TerminalResult)
					require.Nil(t, latest.Status.CompletionTime)
					require.Contains(t, latest.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
					assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationRestore, true)
					require.Nil(t, getRestoreModelCluster(t, namespace, cluster.Name).Status.Restore)
					require.NoError(t, k8sClient.Get(ctx, key, job))
					require.Equal(t, originalJobUID, job.UID)
				}
			}

			// A counted failure can coexist with an active executor. Neither
			// failure nor success counters authorize terminal follow-through.
			job.Status.Active = 1
			if succeeded {
				job.Status.Succeeded = 1
			} else {
				job.Status.Failed = 1
			}
			require.NoError(t, controllerClient.Status().Update(ctx, job))
			assertWaiting()
			job.Status.Active = 0
			require.NoError(t, controllerClient.Status().Update(ctx, job))
			assertWaiting()

			// An owned Pod with no labels still vetoes a terminal Job result.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "restore-executor", Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "restore", Image: "busybox:1.36"}},
				},
			}
			require.NoError(t, k8sClient.Create(ctx, pod))
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "restore", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
			require.NoError(t, k8sClient.Status().Update(ctx, pod))
			setRestoreJobTerminalStatus(job, job.Status.Succeeded, job.Status.Failed)
			require.NoError(t, controllerClient.Status().Update(ctx, job))
			assertWaiting()
			require.Contains(t, getRestoreModelRequest(t, namespace, r.Name).Status.Message, pod.Name)

			// Pod phase alone cannot supply a missing container termination.
			pod.Status.Phase = corev1.PodFailed
			pod.Status.ContainerStatuses = nil
			require.NoError(t, k8sClient.Status().Update(ctx, pod))
			assertWaiting()
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "restore", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}}
			require.NoError(t, k8sClient.Status().Update(ctx, pod))
			step()
			latest := getRestoreModelRequest(t, namespace, r.Name)
			if succeeded {
				require.Equal(t, openbaov1alpha1.RestoreExecutionStageTerminalObserved, latest.Status.Execution.Stage)
				require.Equal(t, openbaov1alpha1.RestorePhaseRunning, latest.Status.Phase)
				assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationRestore, true)
			} else {
				require.Equal(t, openbaov1alpha1.RestoreExecutionStageTerminalObserved, latest.Status.Execution.Stage)
				require.Equal(t, openbaov1alpha1.RestorePhaseFailed, latest.Status.Phase)
				assertRestoreModelLockReleased(t, namespace, cluster.Name)
			}
			wantResult := openbaov1alpha1.RestoreExecutionResultFailed
			if succeeded {
				wantResult = openbaov1alpha1.RestoreExecutionResultSucceeded
			}
			require.Equal(t, wantResult, latest.Status.Execution.TerminalResult)
			if succeeded {
				require.NotNil(t, getRestoreModelCluster(t, namespace, cluster.Name).Status.Restore)
			}
		})
	}
}

func setRestoreJobTerminalStatus(job *batchv1.Job, succeeded, failed int32) {
	now := metav1.Now()
	job.Status.Succeeded, job.Status.Failed = succeeded, failed
	job.Status.Active = 0
	job.Status.Terminating = new(int32(0))
	if job.Status.StartTime == nil {
		job.Status.StartTime = &now
	}
	if succeeded > 0 {
		job.Status.CompletionTime = &now
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}
	} else if failed > 0 {
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
		}
	}
}
