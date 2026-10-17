package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/raftops"
)

// currentLeaderPodByLabel returns the current leader from pod labels, if the
// label state is unambiguous.
func (m *Manager) currentLeaderPodByLabel(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (string, error) {
	podList := &corev1.PodList{}
	if err := m.client.List(ctx, podList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(map[string]string{
			constants.LabelAppInstance: cluster.Name,
			constants.LabelAppName:     constants.LabelValueAppNameOpenBao,
		}),
	); err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	leaderPod, found, err := raftops.FindLeaderPodByLabel(podList.Items)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return leaderPod, nil
}

// stepDownLeader advances the level-triggered leader step-down flow and lets
// the caller decide when to requeue.
func (m *Manager) stepDownLeader(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, podName string, metrics *upgrade.Metrics) (bool, error) {
	if cluster.Status.Upgrade == nil {
		return false, fmt.Errorf("upgrade state is nil")
	}
	if cluster.Status.Upgrade.ExecutionID == "" && (cluster.Status.Upgrade.StartedAt == nil || cluster.Status.Upgrade.StartedAt.IsZero()) {
		return false, fmt.Errorf("legacy rolling upgrade has no start time; set %s to a new value to retry", upgrade.RequestRetryFieldPath)
	}

	stableRevision := upgrade.StableVoterRevision(cluster)
	jobName := rollingStepDownJobName(cluster, podName)
	jobKey := types.NamespacedName{Namespace: cluster.Namespace, Name: jobName}

	var stepDownJob *batchv1.Job
	jobExists := true
	stepDownJob, err := opslifecycle.ReadManagedJob(
		ctx,
		m.jobReader(),
		jobKey,
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		"observe rolling step-down",
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			jobExists = false
		} else {
			return false, fmt.Errorf("failed to get step-down Job %s/%s: %w", cluster.Namespace, jobName, err)
		}
	}
	if jobExists {
		if err := upgrade.ValidateRollingStepDownJob(cluster, stepDownJob, upgrade.RollingStepDownRunID(cluster, podName)); err != nil {
			return false, err
		}
	}

	// Record step-down attempt only once per pod by keying off Job existence.
	if !jobExists {
		targetIsLeader, err := m.isTargetPodLeader(ctx, cluster, podName)
		if err != nil {
			logger.V(1).Info("Unable to confirm target pod leadership before step-down; will retry", "pod", podName, "error", err)
			return false, nil // Requeue
		}
		if !targetIsLeader {
			logger.V(1).Info("Skipping leader step-down because target pod is not leader", "pod", podName)
			return true, nil
		}

		metrics.IncrementStepDownTotal()

		logging.LogAuditEvent(logger, logging.EventStepDownStarted, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"pod":               podName,
			"target_version":    cluster.Status.Upgrade.TargetVersion,
			"from_version":      cluster.Status.Upgrade.FromVersion,
		})
	}

	result, err := upgrade.EnsureExecutorJob(
		ctx,
		m.client,
		m.jobReader(),
		m.scheme,
		logger,
		cluster,
		upgrade.ExecutorActionRollingStepDownLeader,
		upgrade.RollingStepDownRunID(cluster, podName),
		stableRevision,
		"",
		m.clientConfig,
		m.operatorImageVerifier,
		m.Platform,
	)
	if err != nil {
		return false, fmt.Errorf("failed to ensure step-down Job: %w", err)
	}
	if result.Failed {
		metrics.IncrementStepDownFailures()
		return false, fmt.Errorf("step-down Job %s failed", result.Name)
	}
	if result.Running {
		// Time out only while the step-down Job is actively running.
		// A completed Job may still be observed on subsequent reconciles while
		// pod labels/API leadership settle; that must not be treated as timeout.
		if jobExists && !stepDownJob.CreationTimestamp.IsZero() {
			if err := failUpgradeIfTimestampTimeout(cluster, &stepDownJob.CreationTimestamp, stepDownTimeout(podName)); err != nil {
				metrics.IncrementStepDownFailures()
				return false, err
			}
		}
		logger.V(1).Info("Step-down job still running", "pod", podName)
		return false, nil // Requeue
	}

	// Job succeeded - check if the pod we're about to restart is no longer leader.
	pod := &corev1.Pod{}
	if err := m.client.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      podName,
	}, pod); err != nil {
		logger.V(1).Info("Error getting pod after step-down", "error", err)
		return false, nil // Requeue
	}

	stillLeader, present, err := portopenbao.ParseBoolLabel(pod.Labels, portopenbao.LabelActive)
	if err != nil {
		logger.V(1).Info("Invalid OpenBao leader label value after step-down", "error", err)
		return false, nil // Requeue
	}

	if present && !stillLeader {
		logger.Info("Leadership transferred successfully", "previousLeader", podName)
		core.SetStepDownPerformed(&cluster.Status)
		logging.LogAuditEvent(logger, logging.EventStepDownCompleted, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"pod":               podName,
		})
		return true, nil
	}

	// Labels can lag behind reality; confirm leadership via the OpenBao API.
	caCert, err := raftops.LoadClusterCACert(ctx, m.client, cluster)
	if err != nil {
		logger.V(1).Info("CA certificate not available yet while checking leader transfer", "error", err)
		return false, nil // Requeue
	}

	apiClient, err := raftops.NewClusterPodClient(cluster, podName, caCert, m.clientFactory, raftops.ClusterPodClientOptions{})
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			logger.V(1).Info("Transient connection error creating client while checking leader transfer", "error", err)
			return false, nil // Requeue
		}
		return false, fmt.Errorf("failed to create OpenBao client while checking leader transfer: %w", err)
	}

	isLeader, err := apiClient.IsLeader(ctx)
	if err != nil {
		logger.V(1).Info("Leader check failed after step-down; will retry", "pod", podName, "error", err)
		return false, nil // Requeue
	}
	if !isLeader {
		logger.Info("Leadership transferred successfully (verified via API)", "previousLeader", podName)
		core.SetStepDownPerformed(&cluster.Status)
		logging.LogAuditEvent(logger, logging.EventStepDownCompleted, map[string]string{
			"cluster_namespace": cluster.Namespace,
			"cluster_name":      cluster.Name,
			"pod":               podName,
		})
		return true, nil
	}

	// A succeeded step-down Job that still leaves the target pod as leader should
	// not be retried forever. If the target remains leader beyond the timeout
	// window, fail the upgrade and require an explicit retry after operator/user
	// intervention.
	if jobExists && !stepDownJob.CreationTimestamp.IsZero() {
		if err := failUpgradeIfTimestampTimeout(cluster, &stepDownJob.CreationTimestamp, stepDownTimeout(podName)); err != nil {
			metrics.IncrementStepDownFailures()
			return false, err
		}
	}

	logger.V(1).Info("Waiting for leadership transfer", "pod", podName)
	return false, nil // Requeue
}

func rollingStepDownJobName(cluster *openbaov1alpha1.OpenBaoCluster, podName string) string {
	if cluster == nil {
		return ""
	}
	return upgrade.ExecutorJobName(
		cluster.Name,
		upgrade.ExecutorActionRollingStepDownLeader,
		upgrade.RollingStepDownRunID(cluster, podName),
		upgrade.StableVoterRevision(cluster),
		"",
	)
}

func (m *Manager) isTargetPodLeader(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (bool, error) {
	isLeader, err := raftops.IsClusterPodLeader(ctx, m.client, m.clientFactory, cluster, podName, raftops.ClusterPodClientOptions{})
	if err != nil {
		if operatorerrors.IsTransientConnection(err) {
			return false, err
		}
		return false, err
	}

	return isLeader, nil
}
