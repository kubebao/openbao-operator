package upgrade

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

const (
	upgradeJobNamePrefix = "upgrade-"
	upgradeJobTTLSeconds = 3600 // 1 hour TTL for completed/failed jobs

	upgradeTokenVolumeName       = "openbao-token"
	upgradeTokenMountPath        = "/var/run/secrets/tokens" // #nosec G101 -- This is a mount path constant, not a credential
	upgradeTokenFileRelativePath = "openbao-token"
	upgradeTLSCAVolumeName       = "tls-ca"
)

// JobResult contains the status of an upgrade executor Job.
// This is used by both rolling upgrades and blue/green upgrades.
type JobResult struct {
	Name      string
	Exists    bool
	Succeeded bool
	Failed    bool
	Running   bool
}

type executorJobResult struct {
	Name      string
	Succeeded bool
	Failed    bool
	Running   bool
}

// EnsureExecutorJob creates or checks the status of an upgrade executor Job.
// The Job is owned by the OpenBaoCluster and is idempotent by (cluster, action, runID, blueRevision, greenRevision).
func EnsureExecutorJob(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	scheme *runtime.Scheme,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	action ExecutorAction,
	runID string,
	blueRevision string,
	greenRevision string,
	clientConfig portopenbao.ClientConfig,
	operatorImageVerifier imageverify.Verifier,
	platform string,
) (*JobResult, error) {
	result, err := ensureUpgradeExecutorJob(ctx, c, reader, scheme, logger, cluster, action, runID, blueRevision, greenRevision, clientConfig, operatorImageVerifier, platform)
	if err != nil {
		return nil, err
	}
	return &JobResult{
		Name:      result.Name,
		Exists:    true,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Running:   result.Running,
	}, nil
}

// ExecutorJobName returns the deterministic name for an upgrade executor Job.
// This is exported for tests and for other packages that need to refer to the Job name.
func ExecutorJobName(clusterName string, action ExecutorAction, runID string, blueRevision, greenRevision string) string {
	return upgradeExecutorJobName(clusterName, action, runID, blueRevision, greenRevision)
}

func ensureUpgradeExecutorJob(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	scheme *runtime.Scheme,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	action ExecutorAction,
	runID string,
	blueRevision string,
	greenRevision string,
	clientConfig portopenbao.ClientConfig,
	operatorImageVerifier imageverify.Verifier,
	platform string,
) (*executorJobResult, error) {
	if cluster == nil {
		return nil, fmt.Errorf("cluster is required")
	}
	if reader == nil {
		reader = c
	}

	jobName := upgradeExecutorJobName(cluster.Name, action, runID, blueRevision, greenRevision)
	jobKey := types.NamespacedName{Namespace: cluster.Namespace, Name: jobName}

	job, err := opslifecycle.ReadManagedJob(
		ctx,
		reader,
		jobKey,
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		"observe upgrade",
	)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get upgrade Job %s/%s: %w", cluster.Namespace, jobName, err)
		}

		verifiedExecutorDigest := ""
		executorImage, err := resolveUpgradeExecutorImage(cluster, "")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve upgrade executor image for verification: %w", err)
		}

		verifiedExecutorDigest, err = VerifyOperatorImageDigest(
			ctx,
			logger,
			operatorImageVerifier,
			cluster,
			executorImage,
			"",
			"upgrade executor image verification failed",
		)
		if err != nil {
			return nil, err
		}

		job, err := buildUpgradeExecutorJob(cluster, jobName, action, runID, blueRevision, greenRevision, verifiedExecutorDigest, clientConfig, platform)
		if err != nil {
			return nil, fmt.Errorf("failed to build upgrade Job %s/%s: %w", cluster.Namespace, jobName, err)
		}

		if err := opslifecycle.PrepareManagedJobOwner(job, cluster, scheme); err != nil {
			return nil, fmt.Errorf("failed to prepare upgrade Job ownership %s/%s: %w", cluster.Namespace, jobName, err)
		}

		logger.Info("Creating upgrade executor Job", "job", jobName, "action", action, "runID", runID)
		if err := c.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) {
				existing, readErr := opslifecycle.ReadManagedJob(
					ctx,
					reader,
					jobKey,
					cluster,
					openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
					"use existing upgrade",
				)
				if readErr != nil {
					return nil, fmt.Errorf("upgrade Job create collided with an untrusted existing Job: %w", readErr)
				}
				logger.V(1).Info("Upgrade executor Job already exists after create attempt; ownership verified", "job", jobName)
				return observedExecutorResult(cluster, existing, action, runID)
			}
			return nil, fmt.Errorf("failed to create upgrade Job %s/%s: %w", cluster.Namespace, jobName, err)
		}

		return &executorJobResult{
			Name:    jobName,
			Running: true,
		}, nil
	}

	return observedExecutorResult(cluster, job, action, runID)
}

func observedExecutorResult(cluster *openbaov1alpha1.OpenBaoCluster, job *batchv1.Job, action ExecutorAction, runID string) (*executorJobResult, error) {
	if action == ExecutorActionRollingStepDownLeader {
		if err := ValidateRollingStepDownJob(cluster, job, runID); err != nil {
			return nil, err
		}
	}
	return executorResultFromJob(job), nil
}

func executorResultFromJob(job *batchv1.Job) *executorJobResult {
	jobName := job.Name
	if kube.JobSucceeded(job) {
		return &executorJobResult{
			Name:      jobName,
			Succeeded: true,
		}
	}

	if kube.JobFailed(job) {
		return &executorJobResult{
			Name:   jobName,
			Failed: true,
		}
	}

	return &executorJobResult{
		Name:    jobName,
		Running: true,
	}
}
