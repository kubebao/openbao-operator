package rolling

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

	if err := m.cleanupStepDownJobForRetry(ctx, logger, cluster); err != nil {
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

	if err := m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.Status.Upgrade == nil {
			upgrade.MarkRetryRequestHandled(&obj.Status, retryRequest)
			return nil
		}
		clearUpgradeFailureForRetry(obj)
		upgrade.MarkRetryRequestHandled(&obj.Status, retryRequest)
		return nil
	}, adminops.ForceOwnership); err != nil {
		return fmt.Errorf("failed to apply cleared retry status: %w", err)
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

func (m *Manager) cleanupStepDownJobForRetry(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
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

	if err := m.client.Delete(ctx, job, client.Preconditions{UID: &job.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete step-down Job %s/%s for retry cleanup: %w", cluster.Namespace, jobName, err)
	}

	logger.Info("Deleted stale step-down Job before retry", "job", jobName, "pod", targetPod)
	return nil
}

func (m *Manager) resetRolloutPodsForRetry(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return nil
	}
	if cluster.Status.Upgrade.CurrentPartition <= 0 {
		return nil
	}

	sts := &appsv1.StatefulSet{}
	statefulSetName := upgrade.StableVoterStatefulSetName(cluster)
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: statefulSetName}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get StatefulSet %s/%s for retry pod reset: %w", cluster.Namespace, statefulSetName, err)
	}

	resolvedImage := strings.TrimSpace(sts.Annotations[constants.AnnotationResolvedMainImage])
	templateImage := strings.TrimSpace(baoContainerImage(sts.Spec.Template.Spec.Containers))
	if !statefulSetTemplateCurrent(sts, cluster) || resolvedImage == "" || templateImage != resolvedImage {
		logger.Info("Waiting for StatefulSet template to reflect retry target before resetting failed pods",
			"clusterGeneration", cluster.Generation,
			"statefulSetGeneration", sts.Generation,
			"observedGeneration", sts.Status.ObservedGeneration,
			"resolvedImage", resolvedImage,
			"templateImage", templateImage)
		return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf(
			"StatefulSet template has not observed the rendered retry target for cluster generation %d",
			cluster.Generation,
		))
	}

	for _, ordinal := range retryResetOrdinals(cluster) {
		if err := m.resetRolloutPodForRetry(ctx, logger, cluster, sts, ordinal); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) resetRolloutPodForRetry(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	sts *appsv1.StatefulSet,
	ordinal int32,
) error {
	targetPod := upgrade.StableVoterPodName(cluster, ordinal)
	podKey := types.NamespacedName{Namespace: cluster.Namespace, Name: targetPod}

	pod := &corev1.Pod{}
	if err := m.client.Get(ctx, podKey, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get rollout pod %s/%s for retry reset: %w", cluster.Namespace, targetPod, err)
	}

	podRevision := ""
	if pod.Labels != nil {
		podRevision = strings.TrimSpace(pod.Labels[appsv1.StatefulSetRevisionLabel])
	}
	desiredRevision := strings.TrimSpace(sts.Status.UpdateRevision)
	podImage := strings.TrimSpace(baoContainerImage(pod.Spec.Containers))
	desiredImage := strings.TrimSpace(baoContainerImage(sts.Spec.Template.Spec.Containers))

	needsDelete := !isPodReady(pod)
	if desiredRevision != "" && podRevision != desiredRevision {
		needsDelete = true
	}
	if desiredImage != "" && podImage != desiredImage {
		needsDelete = true
	}
	if !needsDelete {
		return nil
	}

	if err := m.client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete rollout pod %s/%s for retry reset: %w", cluster.Namespace, targetPod, err)
	}

	logger.Info("Deleted rollout pod before retry to force a fresh rollout attempt",
		"pod", targetPod,
		"ordinal", ordinal,
		"podRevision", podRevision,
		"targetRevision", desiredRevision,
		"podImage", podImage,
		"desiredImage", desiredImage)
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
