package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

// prepareFailedUpgradeRetry executes a retry selected by decideUpgrade.
// Lock acquisition only refreshes lock status, so the decision and captured
// request still describe the same observed upgrade when preparation starts.
func (m *Manager) prepareFailedUpgradeRetry(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, retryRequest string) error {
	logger.Info("Preparing retry for failed rolling upgrade",
		"retryRequest", retryRequest,
		"targetVersion", cluster.Status.Upgrade.TargetVersion,
		"currentPartition", cluster.Status.Upgrade.CurrentPartition)
	m.emitNormalEvent(cluster, upgrade.ReasonRollingRetryRequested, "Rolling upgrade retry requested for target version %s", cluster.Status.Upgrade.TargetVersion)

	if err := m.retireStepDownJob(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.resetRolloutPodsForRetry(ctx, logger, cluster); err != nil {
		return err
	}

	if err := m.patchRetryStatusSSA(ctx, cluster, retryRequest); err != nil {
		return fmt.Errorf("failed to clear failed upgrade state for retry: %w", err)
	}
	if cluster.Status.Upgrade == nil {
		return nil
	}
	logger.Info("Cleared failed rolling upgrade state and resumed upgrade")
	m.emitNormalEvent(cluster, upgrade.ReasonRollingRetryAccepted, "Rolling upgrade retry accepted for target version %s", cluster.Status.Upgrade.TargetVersion)
	return nil
}

func (m *Manager) patchRetryStatusSSA(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, retryRequest string) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	expected := cluster.Status.Upgrade.DeepCopy()
	executionID := string(uuid.NewUUID())

	if err := m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.Status.UpgradeRequests != nil && obj.Status.UpgradeRequests.LastHandledRetry == retryRequest {
			return nil
		}
		if !equality.Semantic.DeepEqual(obj.Status.Upgrade, expected) ||
			(expected != nil && obj.Spec.Version != expected.TargetVersion) {
			return rollingAttemptChanged()
		}
		if obj.Status.Upgrade == nil {
			upgrade.MarkRetryRequestHandled(&obj.Status, retryRequest)
			return nil
		}
		clearUpgradeFailureForRetry(obj)
		obj.Status.Upgrade.ExecutionID = executionID
		upgrade.MarkRetryRequestHandled(&obj.Status, retryRequest)
		return nil
	}, adminops.ForceOwnership); err != nil {
		return fmt.Errorf("failed to apply cleared retry status: %w", err)
	}
	if expected != nil && cluster.Status.Upgrade == nil {
		return rollingAttemptChanged()
	}
	if expected != nil && cluster.Status.Upgrade.ExecutionID == "" {
		return m.failUnpersistedExecution(ctx, cluster)
	}
	if retryFailureFieldsRemain(cluster) {
		return fmt.Errorf("failed to clear failed rolling-upgrade state via SSA")
	}

	return nil
}

func retryFailureFieldsRemain(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return false
	}
	return upgrade.UpgradeFailed(cluster.Status.Upgrade) ||
		upgrade.UpgradeFailureMessage(cluster.Status.Upgrade) != "" ||
		upgrade.UpgradeFailureAt(cluster.Status.Upgrade) != nil ||
		cluster.Status.Upgrade.LastStepDownTime != nil
}

func (m *Manager) retireStepDownJob(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return nil
	}
	if cluster.Status.Upgrade.CurrentPartition <= 0 {
		return nil
	}

	targetOrdinal := cluster.Status.Upgrade.CurrentPartition - 1
	targetPod := upgrade.StableVoterPodName(cluster, targetOrdinal)
	jobName := rollingStepDownJobName(cluster, targetPod)
	jobKey := types.NamespacedName{Namespace: cluster.Namespace, Name: jobName}

	job, err := opslifecycle.ReadManagedJob(
		ctx,
		m.jobReader(),
		jobKey,
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		"delete stale step-down",
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get step-down Job %s/%s for retry cleanup: %w", cluster.Namespace, jobName, err)
	}

	if job.DeletionTimestamp == nil {
		if err := m.client.Delete(ctx, job, client.Preconditions{UID: &job.UID, ResourceVersion: &job.ResourceVersion},
			client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
			if apierrors.IsConflict(err) {
				return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("step-down Job changed before retirement: %w", err))
			}
			return fmt.Errorf("failed to retire step-down Job %s/%s: %w", cluster.Namespace, jobName, err)
		}
	}
	if err := m.jobReader().Get(ctx, jobKey, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		if err != nil {
			return fmt.Errorf("failed to confirm step-down Job retirement: %w", err)
		}
		return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("waiting for step-down Job %s/%s and its Pods to terminate", cluster.Namespace, jobName))
	}

	logger.Info("Retired step-down Job before replacing upgrade attempt", "job", jobName, "pod", targetPod)
	return nil
}

func retryResetOrdinals(cluster *openbaov1alpha1.OpenBaoCluster) []int32 {
	if cluster == nil || cluster.Status.Upgrade == nil || cluster.Status.Upgrade.CurrentPartition <= 0 {
		return nil
	}

	seen := make(map[int32]struct{})
	ordinals := make([]int32, 0, 1+len(cluster.Status.Upgrade.CompletedPods))
	add := func(ordinal int32) {
		if ordinal < 0 {
			return
		}
		if cluster.Spec.Replicas > 0 && ordinal >= cluster.Spec.Replicas {
			return
		}
		if _, ok := seen[ordinal]; ok {
			return
		}
		seen[ordinal] = struct{}{}
		ordinals = append(ordinals, ordinal)
	}

	add(cluster.Status.Upgrade.CurrentPartition - 1)
	for _, ordinal := range cluster.Status.Upgrade.CompletedPods {
		add(ordinal)
	}

	return ordinals
}

func baoContainerImage(containers []corev1.Container) string {
	for i := range containers {
		if containers[i].Name == constants.ContainerBao {
			return containers[i].Image
		}
	}
	if len(containers) == 0 {
		return ""
	}
	return containers[0].Image
}

func clearUpgradeFailureForRetry(cluster *openbaov1alpha1.OpenBaoCluster) {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return
	}

	now := metav1.Now()
	cluster.Status.Upgrade.Failure = nil
	cluster.Status.Upgrade.LastStepDownTime = nil
	cluster.Status.Upgrade.StartedAt = &now
}
