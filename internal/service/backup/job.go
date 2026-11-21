package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/workloadidentity"
)

const (
	backupJobNamePrefix = "backup-"
	backupJobTTLSeconds = 3600 // 1 hour TTL for completed/failed jobs
)

const (
	awsWebIdentityVolumeName     = "aws-iam-token"
	awsWebIdentityMountPath      = "/var/run/secrets/aws"
	awsWebIdentityTokenFile      = "/var/run/secrets/aws/token" // #nosec G101 -- This is a mount path constant, not a credential
	awsWebIdentityTokenAudience  = "sts.amazonaws.com"          // #nosec G101 -- This is a constant, not a credential
	openBaoTokenVolumeName       = "openbao-token"
	openBaoTokenMountPath        = "/var/run/secrets/tokens" // #nosec G101 -- This is a mount path constant, not a credential
	openBaoTokenFileRelativePath = "openbao-token"
	backupCredentialsVolumeName  = "backup-credentials"          // #nosec G101 -- This is a volume name constant, not a credential
	backupCredentialsMountPath   = "/etc/bao/backup/credentials" // #nosec G101 -- This is a mount path constant, not a credential
	backupTokenVolumeName        = "backup-token"
	backupTokenMountPath         = "/etc/bao/backup/token" // #nosec G101 -- This is a mount path constant, not a credential
	backupTLSCAVolumeName        = "tls-ca"
)

func backupJobFailureMessage(job *batchv1.Job, failureHint string) string {
	message := ""
	if job == nil {
		message = "Backup Job failed."
	} else {
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue && cond.Message != "" {
				message = fmt.Sprintf(
					"Backup Job %s failed: %s. Check kubectl logs job/%s -n %s.",
					job.Name,
					cond.Message,
					job.Name,
					job.Namespace,
				)
				break
			}
		}
	}

	if message == "" && job != nil {
		message = fmt.Sprintf(
			"Backup Job %s failed. Check kubectl logs job/%s -n %s.",
			job.Name,
			job.Name,
			job.Namespace,
		)
	}

	if strings.TrimSpace(failureHint) == "" {
		return message
	}

	return message + " " + strings.TrimSpace(failureHint)
}

func backupFailureMatches(status *openbaov1alpha1.BackupStatus, reason, message string) bool {
	if status == nil {
		return false
	}
	return status.LastFailureReason == strings.TrimSpace(reason) && status.LastFailureMessage == strings.TrimSpace(message)
}

func setBackupFailure(status *openbaov1alpha1.BackupStatus, reason, message string) {
	if status == nil {
		return
	}
	status.LastFailureReason = strings.TrimSpace(reason)
	status.LastFailureMessage = strings.TrimSpace(message)
	now := metav1.Now()
	status.LastFailureTime = &now
}

func clearBackupFailure(status *openbaov1alpha1.BackupStatus) {
	if status == nil {
		return
	}
	status.LastFailureReason = ""
	status.LastFailureMessage = ""
	status.LastFailureTime = nil
}

// ensureBackupJob creates or updates a Kubernetes Job for executing the backup.
// Returns true if a Job was created or is already running, false if backup should not proceed.
// attempt.Time timestamps the backup object key. It must be the Job creation time, not the
// scheduled time: retention ages backups by key timestamp, and a stale scheduled time
// after a blocked period would make a fresh snapshot look old enough to delete.
func (m *Manager) ensureBackupJob(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, jobName string, attempt backupAttemptReceipt) (bool, error) {
	job := &batchv1.Job{}

	err := m.reader.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      jobName,
	}, job)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("failed to get backup Job %s/%s: %w", cluster.Namespace, jobName, err)
		}

		// Job doesn't exist - create it
		logger.Info("Creating backup Job", "job", jobName)

		// Generate the backup key
		// Format: <pathPrefix>/<namespace>/<cluster>/[<filenamePrefix>-]<timestamp>-<uuid>.snap
		backupKey, err := GenerateBackupKey(
			cluster.Spec.Backup.Target.PathPrefix,
			cluster.Namespace,
			cluster.Name,
			"", // filenamePrefix (empty default)
			attempt.Time,
		)
		if err != nil {
			return false, fmt.Errorf("failed to generate backup key: %w", err)
		}

		verifiedExecutorDigest := ""
		executorImage, err := GetBackupExecutorImage(cluster)
		if err != nil {
			return false, fmt.Errorf("failed to get backup executor image: %w", err)
		}
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
				failureMessage := fmt.Sprintf("Backup executor image verification failed: %v", err)
				if failurePolicy == constants.ImageVerificationFailurePolicyBlock {
					if cluster.Status.Backup != nil {
						setBackupFailure(cluster.Status.Backup, constants.ReasonBackupExecutorImageVerificationFailed, failureMessage)
						if patchErr := m.patchStatusSSA(ctx, cluster); patchErr != nil {
							logger.Error(patchErr, "Failed to patch cluster status for image verification failure")
						}
					}
					return false, fmt.Errorf("backup executor image verification failed (policy=Block): %w", err)
				}

				if cluster.Status.Backup != nil {
					setBackupFailure(cluster.Status.Backup, constants.ReasonBackupExecutorImageVerificationFailed, failureMessage)
					if patchErr := m.patchStatusSSA(ctx, cluster); patchErr != nil {
						logger.Error(patchErr, "Failed to patch cluster status for image verification warning warning")
					}
				}
				logger.Error(err, "Backup executor image verification failed but proceeding due to Warn policy", "image", executorImage)
			} else {
				verifiedExecutorDigest = digest
				logger.Info("Backup executor image verified successfully", "digest", digest)
			}
		}

		// Compute StatefulSet name for Blue/Green awareness.
		statefulSetName := cluster.Name
		if cluster.Status.BlueGreen != nil && cluster.Status.BlueGreen.BlueRevision != "" {
			statefulSetName = fmt.Sprintf("%s-%s", cluster.Name, cluster.Status.BlueGreen.BlueRevision)
		}

		job, buildErr := BuildJob(cluster, JobOptions{
			JobName:                jobName,
			JobType:                JobTypeScheduled,
			BackupKey:              backupKey,
			VerifiedExecutorDigest: verifiedExecutorDigest,
			ClientConfig:           m.clientConfig,
			Platform:               m.Platform,
			TargetStatefulSetName:  statefulSetName,
		})

		if buildErr != nil {
			return false, fmt.Errorf("failed to build backup Job %s/%s: %w", cluster.Namespace, jobName, buildErr)
		}
		receipt, err := json.Marshal(attempt)
		if err != nil {
			return false, fmt.Errorf("encode backup attempt receipt: %w", err)
		}
		job.Annotations[backupAttemptAnnotation] = string(receipt)

		// Set OwnerReference for garbage collection
		if err := opslifecycle.PrepareManagedJobOwner(job, cluster, m.scheme); err != nil {
			return false, fmt.Errorf("failed to prepare backup Job ownership %s/%s: %w", cluster.Namespace, jobName, err)
		}

		if err := m.client.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) {
				if _, readErr := opslifecycle.ReadManagedJob(
					ctx,
					m.reader,
					types.NamespacedName{Namespace: cluster.Namespace, Name: jobName},
					cluster,
					openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
					"use existing backup",
				); readErr != nil {
					return false, fmt.Errorf("backup Job create collided with an untrusted existing Job: %w", readErr)
				}
				logger.V(1).Info("Backup Job already exists after create attempt; ownership verified", "job", jobName)
				return true, nil
			}
			return false, fmt.Errorf("failed to create backup Job %s/%s: %w", cluster.Namespace, jobName, backupCreateError(err))
		}

		logger.Info("Backup Job created", "job", jobName, "backupKey", backupKey)
		logging.LogAuditEvent(logger, logging.EventBackupJobCreated, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"job":               jobName,
		})
		if message, ok := workloadidentity.IdentityConfigurationEventMessage(cluster.Spec.Backup.Target, backupServiceAccountName(cluster)); ok {
			m.emitNormalEvent(cluster, ReasonBackupIdentityConfiguration, "%s", message)
		}
		m.emitNormalEvent(cluster, ReasonBackupJobCreated, "Created backup Job %s", jobName)
		return true, nil
	}
	if err := opslifecycle.RequireManagedJobOwner(
		"observe backup",
		job,
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
	); err != nil {
		return false, err
	}

	// Job exists - check its status
	if kube.JobSucceeded(job) {
		logger.Info("Backup Job completed successfully", "job", jobName)
		return false, nil // Job completed, backup manager will process result
	}

	if kube.JobFailed(job) {
		// Check if we should retry (e.g., if it's a transient error)
		// For now, we'll let the Job be cleaned up and a new one created on next reconcile
		logger.Info("Backup Job failed", "job", jobName, "failed", job.Status.Failed)
		return false, nil
	}

	// Job is still running or pending
	logger.V(1).Info("Backup Job is in progress", "job", jobName,
		"active", job.Status.Active,
		"succeeded", job.Status.Succeeded,
		"failed", job.Status.Failed)
	return true, nil
}

type backupJobProcessResult struct {
	completed            bool
	successfulCompletion bool
	statusUpdated        bool
}

// processBackupJobResult processes the result of a completed backup Job and updates cluster status.
func (m *Manager) processBackupJobResult(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, jobName string) (backupJobProcessResult, error) {
	job, err := opslifecycle.ReadManagedJob(ctx, m.reader, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      jobName,
	}, cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"), "process backup")

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Job was cleaned up or doesn't exist
			return backupJobProcessResult{}, nil
		}
		return backupJobProcessResult{}, fmt.Errorf("failed to get backup Job %s/%s: %w", cluster.Namespace, jobName, err)
	}
	return m.processBackupJob(ctx, logger, cluster, job)
}

func (m *Manager) processBackupJob(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	job *batchv1.Job,
) (backupJobProcessResult, error) {
	if job == nil {
		return backupJobProcessResult{}, nil
	}
	if err := opslifecycle.RequireManagedJobOwner(
		"process backup",
		job,
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
	); err != nil {
		return backupJobProcessResult{}, err
	}
	jobName := job.Name
	// Check Job status
	if kube.JobSucceeded(job) {
		// Job succeeded - extract result from Job annotations
		// The backup key is stored in the Job annotation by the manager when creating the job
		backupKey := strings.TrimSpace(job.Annotations["openbao.org/backup-key"])
		if backupKey == "" {
			return backupJobProcessResult{}, fmt.Errorf(
				"refusing to process successful backup Job %s/%s without openbao.org/backup-key",
				job.Namespace,
				job.Name,
			)
		}

		// Check if we've already processed this specific job success
		// to avoid updating status on every reconcile
		if cluster.Status.Backup.LastBackupName == backupKey && backupKey != "" {
			// Already processed this job success, don't update status again
			logger.V(1).Info("Backup Job success already processed", "job", jobName, "backupKey", backupKey)
			return backupJobProcessResult{completed: true, successfulCompletion: true}, nil
		}

		// Skip processing successful jobs that are older than the last recorded backup.
		// This prevents re-processing old successful jobs if they're still in the job list.
		if cluster.Status.Backup.LastBackupTime != nil && job.CreationTimestamp.Before(cluster.Status.Backup.LastBackupTime) {
			logger.V(1).Info("Skipping stale successful backup job (older than last backup time)", "job", jobName)
			return backupJobProcessResult{completed: true}, nil
		}

		summary, err := m.snapshotSummary(ctx, job)
		if err != nil {
			return backupJobProcessResult{}, err
		}
		cluster.Status.Backup.LatestSnapshot = summary
		cluster.Status.Backup.SuccessfulBackups++
		now := metav1.Now()
		cluster.Status.Backup.LastBackupTime = &now
		if backupKey != "" {
			cluster.Status.Backup.LastBackupName = backupKey
		}
		cluster.Status.Backup.ConsecutiveFailures = 0
		clearBackupFailure(cluster.Status.Backup)

		if err := m.patchStatusSSA(ctx, cluster); err != nil {
			return backupJobProcessResult{}, fmt.Errorf("failed to patch backup status (success): %w", err)
		}

		logger.Info("Backup Job completed successfully, status updated", "job", jobName, "lastBackupTime", now, "backupKey", backupKey)
		logging.LogAuditEvent(logger, logging.EventBackupJobSucceeded, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"job":               jobName,
		})
		m.emitNormalEvent(cluster, ReasonBackupCompleted, "Backup completed successfully from Job %s", jobName)
		return backupJobProcessResult{
			completed:            true,
			successfulCompletion: true,
			statusUpdated:        true,
		}, nil
	}

	if kube.JobFailed(job) {
		// Job failed - check if we've already processed this specific job failure
		// to avoid incrementing ConsecutiveFailures on every reconcile
		failureHint := ""
		if cluster.Spec.Backup != nil {
			failureHint = workloadidentity.FailureHint(cluster.Spec.Backup.Target, backupServiceAccountName(cluster))
		}
		expectedFailureMessage := backupJobFailureMessage(job, failureHint)
		if backupFailureMatches(cluster.Status.Backup, ReasonBackupFailed, expectedFailureMessage) {
			// Already processed this job failure, don't update status again
			logger.V(1).Info("Backup Job failure already processed", "job", jobName)
			return backupJobProcessResult{completed: true}, nil
		}

		// Skip processing failed jobs that are older than the last successful backup.
		// This prevents re-processing old failures after a successful backup clears the failure status.
		if cluster.Status.Backup.LastBackupTime != nil && job.CreationTimestamp.Before(cluster.Status.Backup.LastBackupTime) {
			logger.V(1).Info("Skipping stale failed backup job (older than last successful backup)", "job", jobName)
			return backupJobProcessResult{completed: true}, nil
		}

		cluster.Status.Backup.ConsecutiveFailures++
		setBackupFailure(cluster.Status.Backup, ReasonBackupFailed, expectedFailureMessage)

		if err := m.patchStatusSSA(ctx, cluster); err != nil {
			return backupJobProcessResult{}, fmt.Errorf("failed to patch backup status (failed): %w", err)
		}

		logger.Error(fmt.Errorf("backup job failed"), "Backup Job failed, status updated",
			"job", jobName,
			"consecutiveFailures", cluster.Status.Backup.ConsecutiveFailures)
		logging.LogAuditEvent(logger, logging.EventBackupJobFailed, map[string]string{
			"cluster_namespace":    cluster.Namespace,
			"cluster_name":         cluster.Name,
			"job":                  jobName,
			"consecutive_failures": fmt.Sprintf("%d", cluster.Status.Backup.ConsecutiveFailures),
		})
		m.emitWarningEvent(cluster, ReasonBackupFailed, "Backup Job %s failed", jobName)
		return backupJobProcessResult{completed: true, statusUpdated: true}, nil
	}

	// Job is still running
	return backupJobProcessResult{}, nil
}

func backupJobName(cluster *openbaov1alpha1.OpenBaoCluster, scheduledTime time.Time) string {
	return backupJobNamePrefix + cluster.Name + "-" + scheduledTime.UTC().Format("20060102-150405")
}

// NOTE: buildBackupJob, buildBackupJobVolumeMounts, buildBackupJobVolumes, and getBackupExecutorImage
// have been moved to job_builder.go as BuildJob, BuildBackupJobVolumeMounts, BuildBackupJobVolumes,
// and GetBackupExecutorImage respectively.
