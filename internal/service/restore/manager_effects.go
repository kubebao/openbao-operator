package restore

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/adapter/security"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/dc-tec/openbao-operator/internal/platform/errors"
	"github.com/dc-tec/openbao-operator/internal/platform/logging"
	"github.com/dc-tec/openbao-operator/internal/service/opslifecycle"
	"github.com/dc-tec/openbao-operator/internal/service/workloadidentity"
)

// handleRunning manages the restore Job and checks for completion.
func (m *Manager) handleRunning(ctx context.Context, logger logr.Logger, restore *openbaov1alpha1.OpenBaoRestore) (ctrl.Result, error) {
	observation, err := m.observeRestore(ctx, restore)
	if err != nil {
		return ctrl.Result{}, err
	}
	return m.applyRestoreDecision(ctx, logger, restore, observation, decideRestore(observation.state))
}

func (m *Manager) applyRestoreDecision(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	observation restoreObservation,
	decision restoreDecision,
) (ctrl.Result, error) {
	switch decision.kind {
	case restoreDecisionIdle:
		return ctrl.Result{}, nil
	case restoreDecisionMarkUnknown:
		return ctrl.Result{}, m.markRestoreExecutionUnknown(ctx, restore, decision.message)
	case restoreDecisionCreateJob:
		if done, err := m.renewRunningRestoreLock(ctx, logger, restore, observation.cluster); done || err != nil {
			return ctrl.Result{}, err
		}
		return m.createRestoreJob(ctx, logger, restore, observation.cluster, restore.Status.Execution.JobName)
	case restoreDecisionAdoptLegacyJob:
		return m.adoptLegacyRestoreJob(ctx, logger, restore, observation)
	case restoreDecisionRecordCreatedReceipt:
		if err := m.markRestoreExecutionCreated(ctx, restore, observation.job); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to persist restore Job creation receipt: %w", err)
		}
		return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
	case restoreDecisionRecordSucceededJob:
		if err := m.markRestoreExecutionTerminal(ctx, restore, openbaov1alpha1.RestoreExecutionResultSucceeded); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to persist successful restore Job receipt: %w", err)
		}
		return m.continueRestoreRecovery(ctx, logger, restore, observation.cluster)
	case restoreDecisionRecordFailedJob:
		if done, err := m.renewRunningRestoreLock(ctx, logger, restore, observation.cluster); done || err != nil {
			return ctrl.Result{}, err
		}
		if err := m.markRestoreExecutionTerminal(ctx, restore, openbaov1alpha1.RestoreExecutionResultFailed); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to persist failed restore Job receipt: %w", err)
		}
		return m.failRestore(ctx, logger, restore, restoreJobFailedStatusMessage(observation.job, workloadidentity.FailureHint(restore.Spec.Source.Target, restoreServiceAccountName(observation.cluster))))
	case restoreDecisionPollJob:
		if done, err := m.renewRunningRestoreLock(ctx, logger, restore, observation.cluster); done || err != nil {
			return ctrl.Result{}, err
		}
		original := restore.DeepCopy()
		restore.Status.Message = restoreJobRunningStatusMessage(observation.job.Name)
		if observation.state.waitMessage != "" {
			restore.Status.Message = observation.state.waitMessage
		}
		if err := m.patchStatus(ctx, restore, original); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch restore status while job is running: %w", err)
		}
		return ctrl.Result{RequeueAfter: restoreRequeueJobPoll}, nil
	case restoreDecisionContinueRecovery:
		return m.continueRestoreRecovery(ctx, logger, restore, observation.cluster)
	case restoreDecisionFailRestore:
		return m.failRestore(ctx, logger, restore, "Restore Job failed. The terminal execution receipt is preserved; inspect the retained Job logs before creating a new OpenBaoRestore.")
	case restoreDecisionCompleteRestore:
		return ctrl.Result{}, m.completeRestore(ctx, logger, restore, "Restore completed successfully after post-restore recovery")
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported restore decision %d", decision.kind)
	}
}

func (m *Manager) adoptLegacyRestoreJob(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	observation restoreObservation,
) (ctrl.Result, error) {
	operationID := restoreExecutionOperationID(restore)
	original := restore.DeepCopy()
	now := metav1.Now()
	restore.Status.Execution = &openbaov1alpha1.RestoreExecutionStatus{
		OperationID: operationID,
		Stage:       openbaov1alpha1.RestoreExecutionStageCreated,
		JobName:     observation.job.Name,
		JobUID:      observation.job.UID,
		PreparedAt:  &now,
		CommittedAt: &now,
		CreatedAt:   &now,
	}
	restore.Status.Message = fmt.Sprintf("Adopted existing restore Job %s and persisted its execution receipt.", observation.job.Name)
	if err := m.patchStatus(ctx, restore, original); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to persist legacy restore Job receipt: %w", err)
	}
	logger.Info("Adopted existing restore Job without a prior execution receipt", "job", observation.job.Name, "operationID", operationID)

	nextObservation, err := m.observeRestoreJob(ctx, restore, restoreObservation{
		cluster: observation.cluster,
		state:   restoreState{executionStage: openbaov1alpha1.RestoreExecutionStageCreated},
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	return m.applyRestoreDecision(ctx, logger, restore, nextObservation, decideRestore(nextObservation.state))
}

func (m *Manager) continueRestoreRecovery(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (ctrl.Result, error) {
	if err := m.handleSucceededRestoreJob(ctx, logger, restore, cluster); err != nil {
		return ctrl.Result{}, err
	}
	if restore.Status.Execution.Stage == openbaov1alpha1.RestoreExecutionStageFollowThroughComplete {
		if err := m.completeRestore(ctx, logger, restore, "Restore completed successfully after post-restore recovery"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
}

func (m *Manager) renewRunningRestoreLock(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (bool, error) {
	lock := restoreOperationLock(restore)
	lockHeldByUs := lock.IsHeldBy(cluster.Status.OperationLock)
	if err := opslifecycle.AcquireWithReader(ctx, m.reader, m.client, cluster, lock, opslifecycle.AcquireOptions{
		Message: restoreLockMessage(restore),
	}); err != nil {
		if opslifecycle.IsLockHeld(err) {
			logging.LogAuditEvent(logger, logging.EventRestoreLockLost, map[string]string{
				"cluster_namespace": restore.Namespace,
				"cluster_name":      restore.Spec.Cluster,
				"restore_name":      restore.Name,
			})
			m.emitWarningEvent(restore, ReasonOperationLockLost, "Restore lost the cluster operation lock while running")
			if restoreExecutionCommitted(restore.Status.Execution) {
				logger.Info("Restore lost the operation lock after execution commitment; continuing observation and post-restore recovery",
					"operationID", restore.Status.Execution.OperationID,
					"executionStage", restore.Status.Execution.Stage)
				return false, nil
			}
			_, failErr := m.failRestore(
				ctx,
				logger,
				restore,
				"Restore stopped because another operation took the cluster operation lock while the restore Job was running. Check concurrent backup or upgrade activity, then create a new OpenBaoRestore to retry.",
			)
			return true, failErr
		}
		return false, fmt.Errorf("failed to renew cluster operation lock: %w", err)
	}
	if !lockHeldByUs {
		logging.LogAuditEvent(logger, logging.EventOperationLockAcquired, map[string]string{
			"cluster_namespace": restore.Namespace,
			"cluster_name":      restore.Spec.Cluster,
			"restore_name":      restore.Name,
			"operation":         string(openbaov1alpha1.ClusterOperationRestore),
			"holder":            lock.Holder,
		})
	}

	return false, nil
}

func (m *Manager) handleSucceededRestoreJob(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
) error {
	if !clusterRestoreRestartCompleted(cluster, restore) {
		done, err := m.renewRunningRestoreLock(ctx, logger, restore, cluster)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		requested, err := m.requestPostRestoreRestart(ctx, cluster, restore)
		if err != nil {
			return err
		}
		if requested {
			m.emitNormalEvent(restore, ReasonRestoreRestartRequested, "Requested voter Pod restart after snapshot application")
		}

		complete, message, err := m.postRestoreVoterRestartComplete(ctx, cluster, restore)
		if err != nil {
			return err
		}
		if !complete {
			return m.patchRestoreProgressMessage(ctx, restore, message)
		}

		marked, err := m.markPostRestoreRestartCompleted(ctx, cluster, restore)
		if err != nil {
			return err
		}
		if marked {
			m.emitNormalEvent(restore, ReasonRestoreRestartCompleted, "Voter Pods completed the post-restore restart")
		}
	}

	if !shouldWaitForSteadyReadReplicaRestore(cluster) {
		return m.markRestoreFollowThroughComplete(ctx, restore)
	}

	if steadyReadReplicaRestoreComplete(cluster) {
		return m.markRestoreFollowThroughComplete(ctx, restore)
	}

	readyReplicas := int32(0)
	registeredReplicas := int32(0)
	if cluster.Status.ReadReplicas != nil {
		readyReplicas = cluster.Status.ReadReplicas.ReadyReplicas
		registeredReplicas = cluster.Status.ReadReplicas.RegisteredReplicas
	}

	original := restore.DeepCopy()
	restore.Status.Message = fmt.Sprintf(
		"Waiting for steady read replicas to restore before marking restore complete: desiredReadReplicas=%d readyReadReplicas=%d registeredReadReplicas=%d readReplicasReady=%t readServingAvailable=%t raftMembershipReady=%t",
		cluster.Spec.ReadReplicas.Replicas,
		readyReplicas,
		registeredReplicas,
		restoreConditionTrue(cluster, openbaov1alpha1.ConditionReadReplicasReady),
		restoreConditionTrue(cluster, openbaov1alpha1.ConditionReadServingAvailable),
		restoreConditionTrue(cluster, openbaov1alpha1.ConditionRaftMembershipReady),
	)
	if err := m.patchStatus(ctx, restore, original); err != nil {
		return fmt.Errorf("failed to patch restore status while waiting for steady read replicas to restore: %w", err)
	}

	return nil
}

func (m *Manager) createRestoreJob(
	ctx context.Context,
	logger logr.Logger,
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
	jobName string,
) (ctrl.Result, error) {
	if restore.Status.Execution == nil || restore.Status.Execution.Stage != openbaov1alpha1.RestoreExecutionStagePrepared {
		return ctrl.Result{}, fmt.Errorf("restore execution must be Prepared before Job creation")
	}
	if err := validateRestoreExecutionIdentity(restore); err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid prepared restore execution: %w", err)
	}

	executorImage, err := getRestoreExecutorImage(restore, cluster)
	if err != nil {
		return m.failRestore(ctx, logger, restore, fmt.Sprintf("failed to determine restore executor image: %v", err))
	}

	verifiedExecutorDigest := ""
	if executorImage != "" && security.IsOperatorImageVerificationEnabled(cluster) {
		verifyCtx, cancel := context.WithTimeout(ctx, constants.ImageVerificationTimeout)
		defer cancel()

		digest, err := security.VerifyOperatorImageForCluster(verifyCtx, logger, m.operatorImageVerifier, cluster, executorImage)
		if err != nil {
			failurePolicy := ""
			if cluster.Spec.OperatorImageVerification != nil {
				failurePolicy = cluster.Spec.OperatorImageVerification.FailurePolicy
			}
			if failurePolicy == "" {
				failurePolicy = constants.ImageVerificationFailurePolicyBlock
			}
			if failurePolicy == constants.ImageVerificationFailurePolicyBlock {
				if operatorerrors.IsTransient(err) {
					original := restore.DeepCopy()
					restore.Status.Message = fmt.Sprintf("Waiting for restore executor image verification before creating the restore Job: %v", err)
					if statusErr := m.patchStatus(ctx, restore, original); statusErr != nil {
						return ctrl.Result{}, fmt.Errorf("failed to patch restore status after transient image verification failure: %w", statusErr)
					}
					return ctrl.Result{RequeueAfter: restoreRequeueJobPoll}, nil
				}
				return m.failRestore(
					ctx,
					logger,
					restore,
					fmt.Sprintf("Restore executor image verification failed: %v. Check image verification configuration or use failurePolicy=Warn if that is intended, then create a new OpenBaoRestore to retry.", err),
				)
			}
			logger.Error(err, "Restore executor image verification failed but proceeding due to Warn policy", "image", executorImage)
		} else {
			verifiedExecutorDigest = digest
			logger.Info("Restore executor image verified successfully", "digest", digest)
		}
	}

	job, err := m.buildRestoreJob(restore, cluster, verifiedExecutorDigest)
	if err != nil {
		return m.failRestore(ctx, logger, restore, fmt.Sprintf("failed to build restore job: %v", err))
	}

	if err := opslifecycle.PrepareManagedJobOwner(job, restore, m.scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to prepare restore Job ownership: %w", err)
	}
	if err := m.markRestoreExecutionCommitted(ctx, restore); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to persist restore Job creation commitment: %w", err)
	}

	if err := m.client.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, readErr := opslifecycle.ReadManagedJob(
				ctx,
				m.reader,
				types.NamespacedName{Namespace: restore.Namespace, Name: jobName},
				restore,
				openbaov1alpha1.GroupVersion.WithKind("OpenBaoRestore"),
				"use existing restore",
			)
			if readErr != nil {
				return ctrl.Result{}, fmt.Errorf("restore Job create collided with an untrusted existing Job: %w", readErr)
			}
			if receiptErr := m.markRestoreExecutionCreated(ctx, restore, existing); receiptErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to persist existing restore Job creation receipt: %w", receiptErr)
			}
			logger.V(1).Info("Restore job already exists after create attempt; ownership verified", "job", jobName)
			return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
		}

		existing, readErr := opslifecycle.ReadManagedJob(
			ctx,
			m.reader,
			types.NamespacedName{Namespace: restore.Namespace, Name: jobName},
			restore,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoRestore"),
			"resolve ambiguous restore create",
		)
		if readErr == nil {
			if receiptErr := m.markRestoreExecutionCreated(ctx, restore, existing); receiptErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to persist restore Job receipt after ambiguous create: %w", receiptErr)
			}
			return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
		}
		if !apierrors.IsNotFound(readErr) {
			return ctrl.Result{}, fmt.Errorf("failed to resolve restore Job create error %v: %w", err, readErr)
		}

		message := fmt.Sprintf("Restore Job creation returned an error after execution %s was committed: %v. The Job is not observable, so its execution state is unknown and the operator will not retry creation.", restore.Status.Execution.OperationID, err)
		if statusErr := m.markRestoreExecutionUnknown(ctx, restore, message); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to record ambiguous restore Job creation after error %v: %w", err, statusErr)
		}
		return ctrl.Result{}, nil
	}
	if err := m.markRestoreExecutionCreated(ctx, restore, job); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to persist restore Job creation receipt: %w", err)
	}

	logger.Info("Created restore job", "job", jobName)
	logging.LogAuditEvent(logger, logging.EventRestoreJobCreated, map[string]string{
		"cluster_namespace": restore.Namespace,
		"cluster_name":      restore.Spec.Cluster,
		"restore_name":      restore.Name,
		"job":               jobName,
	})
	if message, ok := workloadidentity.IdentityConfigurationEventMessage(restore.Spec.Source.Target, restoreServiceAccountName(cluster)); ok {
		m.emitNormalEvent(restore, ReasonRestoreIdentityConfiguration, "%s", message)
	}
	m.emitNormalEvent(restore, ReasonRestoreJobCreated, "Created restore Job %s", jobName)

	return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
}
