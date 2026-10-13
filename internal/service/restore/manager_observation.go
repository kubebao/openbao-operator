package restore

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/service/opslifecycle"
)

type restoreObservation struct {
	state   restoreState
	cluster *openbaov1alpha1.OpenBaoCluster
	job     *batchv1.Job
}

func (m *Manager) observeRestore(
	ctx context.Context,
	restore *openbaov1alpha1.OpenBaoRestore,
) (restoreObservation, error) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := m.reader.Get(ctx, types.NamespacedName{
		Namespace: restore.Namespace,
		Name:      restore.Spec.Cluster,
	}, cluster); err != nil {
		return restoreObservation{}, fmt.Errorf("failed to get target cluster: %w", err)
	}

	if restore.Status.Execution == nil {
		return m.observeLegacyRestoreJob(ctx, restore, cluster)
	}
	if err := validateRestoreExecutionIdentity(restore); err != nil {
		return unknownRestoreObservation(
			cluster,
			fmt.Sprintf("Restore execution identity is inconsistent: %v. The operator will not create or recreate a restore Job. Investigate the existing Job and delete this OpenBaoRestore only after the cluster state is known.", err),
		), nil
	}

	observation := restoreObservation{
		cluster: cluster,
		state: restoreState{
			executionStage: restore.Status.Execution.Stage,
			terminalResult: restore.Status.Execution.TerminalResult,
		},
	}

	switch observation.state.executionStage {
	case openbaov1alpha1.RestoreExecutionStageCommitted, openbaov1alpha1.RestoreExecutionStageCreated:
		return m.observeRestoreJob(ctx, restore, observation)
	default:
		return observation, nil
	}
}

func (m *Manager) observeLegacyRestoreJob(
	ctx context.Context,
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (restoreObservation, error) {
	job, err := opslifecycle.ReadManagedJob(ctx, m.reader, types.NamespacedName{
		Namespace: restore.Namespace,
		Name:      restoreJobName(restore),
	}, restore, openbaov1alpha1.GroupVersion.WithKind("OpenBaoRestore"), "observe restore")
	if apierrors.IsNotFound(err) {
		return unknownRestoreObservation(
			cluster,
			"Restore is Running without an execution receipt and its Job is missing. The Job may have completed before the controller recorded it, so the operator will not recreate it. Verify the cluster state, then delete this OpenBaoRestore to release the operation lock.",
		), nil
	}
	if err != nil {
		return restoreObservation{}, fmt.Errorf("failed to get restore job: %w", err)
	}

	operationID := restoreExecutionOperationID(restore)
	if jobOperationID := job.Annotations[restoreExecutionIDAnnotation]; jobOperationID != "" && jobOperationID != operationID {
		return unknownRestoreObservation(
			cluster,
			fmt.Sprintf("Existing restore Job %s has operation ID %q, expected %q. The operator will not adopt or recreate it.", job.Name, jobOperationID, operationID),
		), nil
	}

	return restoreObservation{
		cluster: cluster,
		job:     job,
		state:   restoreState{legacy: true},
	}, nil
}

func (m *Manager) observeRestoreJob(
	ctx context.Context,
	restore *openbaov1alpha1.OpenBaoRestore,
	observation restoreObservation,
) (restoreObservation, error) {
	committed := observation.state.executionStage == openbaov1alpha1.RestoreExecutionStageCommitted
	operation := "observe restore"
	jobDescription := "restore Job"
	if committed {
		operation = "observe committed restore"
		jobDescription = "committed restore Job"
	}
	job, err := opslifecycle.ReadManagedJob(ctx, m.reader, types.NamespacedName{
		Namespace: restore.Namespace,
		Name:      restore.Status.Execution.JobName,
	}, restore, openbaov1alpha1.GroupVersion.WithKind("OpenBaoRestore"), operation)
	if apierrors.IsNotFound(err) {
		if committed {
			observation.state.unknownMessage = fmt.Sprintf("Committed restore Job %s is missing before a creation receipt was persisted. Its execution result is unknown, so the operator will not recreate it. Verify the cluster state, then delete this OpenBaoRestore to release the operation lock.", restore.Status.Execution.JobName)
		} else {
			observation.state.unknownMessage = fmt.Sprintf("Restore Job %s is missing after its creation receipt was persisted. Its execution result is unknown, so the operator will not recreate it. Verify the cluster state, then delete this OpenBaoRestore to release the operation lock.", restore.Status.Execution.JobName)
		}
		return observation, nil
	}
	if err != nil {
		return restoreObservation{}, fmt.Errorf("failed to get %s: %w", jobDescription, err)
	}
	if err := validateRestoreExecutionJob(restore.Status.Execution, job); err != nil {
		if committed {
			observation.state.unknownMessage = fmt.Sprintf("Committed restore Job identity is inconsistent: %v. The operator will not recreate it.", err)
		} else {
			observation.state.unknownMessage = fmt.Sprintf("Restore Job identity no longer matches its creation receipt: %v. The operator will not recreate it.", err)
		}
		return observation, nil
	}

	observation.job = job
	observation.state.jobState = classifyRestoreJob(job)
	if observation.state.jobState == restoreJobSucceeded || observation.state.jobState == restoreJobFailed {
		pending, err := m.restoreExecutorPending(ctx, job)
		if err != nil {
			return restoreObservation{}, err
		}
		if pending != "" {
			observation.state.jobState = restoreJobRunning
			observation.state.waitMessage = pending
		}
	}
	return observation, nil
}

func classifyRestoreJob(job *batchv1.Job) restoreJobState {
	if job == nil || job.Status.Active != 0 || (job.Status.Terminating != nil && *job.Status.Terminating != 0) {
		return restoreJobRunning
	}
	var succeeded, failed bool
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			succeeded = true
		case batchv1.JobFailed:
			failed = true
		}
	}
	// Pod counters and interim conditions do not close a Job. Conflicting
	// terminal conditions also cannot authorize recovery or lock release.
	switch {
	case succeeded && !failed:
		return restoreJobSucceeded
	case failed && !succeeded:
		return restoreJobFailed
	default:
		return restoreJobRunning
	}
}

func unknownRestoreObservation(
	cluster *openbaov1alpha1.OpenBaoCluster,
	message string,
) restoreObservation {
	return restoreObservation{
		cluster: cluster,
		state:   restoreState{unknownMessage: message},
	}
}
