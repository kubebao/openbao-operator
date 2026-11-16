package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/raftops"
)

type clusterHealthCounts struct {
	healthyPods int
	leaderCount int
}

// verifyClusterHealth checks that the cluster is in a state suitable for upgrades.
func (m *Manager) verifyClusterHealth(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	sts, err := m.getUpgradeStatefulSet(ctx, cluster)
	if err != nil {
		return err
	}

	if sts.Status.ReadyReplicas != cluster.Spec.Replicas {
		return transientClusterStatef(
			upgrade.ReasonClusterNotReady,
			"not all replicas are ready (%d/%d)",
			sts.Status.ReadyReplicas,
			cluster.Spec.Replicas,
		)
	}

	podList, err := m.getClusterPods(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to list cluster pods: %w", err)
	}
	if len(podList) != int(cluster.Spec.Replicas) {
		return transientClusterStatef(
			upgrade.ReasonClusterNotReady,
			"unexpected number of pods (%d/%d)",
			len(podList),
			cluster.Spec.Replicas,
		)
	}

	counts, err := m.requireHealthyLeaderQuorum(ctx, logger, cluster, podList)
	if err != nil {
		return err
	}

	logger.Info("Cluster health verified",
		"healthyPods", counts.healthyPods,
		"totalPods", cluster.Spec.Replicas,
		"leaderCount", counts.leaderCount)

	return nil
}

// verifyResumeClusterHealth checks the minimum cluster safety required to
// continue an in-progress rolling upgrade. Unlike verifyClusterHealth, it does
// not require every replica to be ready because the currently updating pod may
// legitimately be unavailable while the rollout is in flight.
func (m *Manager) verifyResumeClusterHealth(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	sts, err := m.getUpgradeStatefulSet(ctx, cluster)
	if err != nil {
		return err
	}

	targetPodName := currentResumeTargetPodName(cluster)
	quorumRequired := requiredQuorum(cluster.Spec.Replicas)
	if err := ensureResumeReadyReplicaQuorum(cluster, targetPodName, sts.Status.ReadyReplicas, quorumRequired); err != nil {
		return err
	}

	podList, err := m.getClusterPods(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to list cluster pods: %w", err)
	}
	if err := ensureResumePodQuorum(cluster, targetPodName, len(podList), quorumRequired); err != nil {
		return err
	}

	if err := m.verifyNonTargetPodsReadyAndHealthy(ctx, logger, cluster, podList, targetPodName); err != nil {
		return err
	}

	counts, err := m.requireHealthyLeaderQuorum(ctx, logger, cluster, podList)
	if err != nil {
		return err
	}

	logger.Info("Rolling upgrade resume health verified",
		"healthyPods", counts.healthyPods,
		"readyReplicas", sts.Status.ReadyReplicas,
		"totalPods", cluster.Spec.Replicas,
		"targetPod", targetPodName,
		"leaderCount", counts.leaderCount)

	return nil
}

func (m *Manager) getUpgradeStatefulSet(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{}
	stsName := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      upgrade.StableVoterStatefulSetName(cluster),
	}
	if err := m.client.Get(ctx, stsName, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, transientClusterStatef(upgrade.ReasonClusterNotReady, "StatefulSet not found; cluster may not be fully initialized")
		}
		return nil, fmt.Errorf("failed to get StatefulSet: %w", err)
	}
	return sts, nil
}

func currentResumeTargetPodName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster == nil || cluster.Status.Upgrade == nil || cluster.Status.Upgrade.CurrentPartition <= 0 {
		return ""
	}
	return upgrade.StableVoterPodName(cluster, cluster.Status.Upgrade.CurrentPartition-1)
}

func requiredQuorum(replicas int32) int {
	return int((replicas / 2) + 1)
}

func ensureResumeReadyReplicaQuorum(cluster *openbaov1alpha1.OpenBaoCluster, targetPodName string, readyReplicas int32, quorumRequired int) error {
	if int(readyReplicas) >= quorumRequired {
		return nil
	}
	msg := fmt.Sprintf("rolling upgrade cannot continue without quorum-ready replicas (%d/%d ready, need %d)",
		readyReplicas, cluster.Spec.Replicas, quorumRequired)
	if err := markResumeUpgradeTimeout(cluster, targetPodName); err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	return transientClusterStatef(upgrade.ReasonClusterNotReady, "%s", msg)
}

func ensureResumePodQuorum(cluster *openbaov1alpha1.OpenBaoCluster, targetPodName string, podCount int, quorumRequired int) error {
	if podCount >= quorumRequired {
		return nil
	}
	msg := fmt.Sprintf("rolling upgrade cannot continue with too few cluster pods (%d/%d, need at least %d)",
		podCount, cluster.Spec.Replicas, quorumRequired)
	if err := markResumeUpgradeTimeout(cluster, targetPodName); err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	return transientClusterStatef(upgrade.ReasonClusterNotReady, "%s", msg)
}

func (m *Manager) requireHealthyLeaderQuorum(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	pods []corev1.Pod,
) (clusterHealthCounts, error) {
	counts, err := m.checkPodHealth(ctx, logger, cluster, pods)
	if err != nil {
		return clusterHealthCounts{}, fmt.Errorf("failed to check pod health: %w", err)
	}

	quorumRequired := requiredQuorum(cluster.Spec.Replicas)
	if counts.healthyPods < quorumRequired {
		return clusterHealthCounts{}, transientClusterStatef(
			upgrade.ReasonQuorumLost,
			"cluster has lost quorum (%d/%d healthy, need %d)",
			counts.healthyPods,
			cluster.Spec.Replicas,
			quorumRequired,
		)
	}
	if counts.leaderCount == 0 {
		return clusterHealthCounts{}, transientClusterStatef(upgrade.ReasonLeaderUnknown, "no leader found in cluster")
	}
	if counts.leaderCount > 1 {
		return clusterHealthCounts{}, fmt.Errorf("multiple leaders detected (%d); possible split-brain", counts.leaderCount)
	}

	return counts, nil
}

func markResumeUpgradeTimeout(cluster *openbaov1alpha1.OpenBaoCluster, podName string) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return nil
	}
	if podName == "" {
		podName = "upgrade-target"
	}
	return failUpgradeIfStartedTimeout(cluster, podReadyTimeout(podName, cluster))
}

func (m *Manager) verifyNonTargetPodsReadyAndHealthy(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	pods []corev1.Pod,
	targetPodName string,
) error {
	podsByName := make(map[string]*corev1.Pod, len(pods))
	for i := range pods {
		pod := &pods[i]
		podsByName[pod.Name] = pod
	}

	caCert, err := raftops.LoadClusterCACert(ctx, m.client, cluster)
	if err != nil {
		return fmt.Errorf("failed to get CA certificate: %w", err)
	}

	for ordinal := int32(0); ordinal < cluster.Spec.Replicas; ordinal++ {
		podName := upgrade.StableVoterPodName(cluster, ordinal)
		if podName == targetPodName {
			continue
		}

		pod := podsByName[podName]
		if pod == nil {
			return resumePodReadyBlocker(cluster, podName, "rolling upgrade cannot continue while non-target pod %s is missing; current target is %s", podName, targetPodName)
		}
		if !isPodReady(pod) {
			return resumePodReadyBlocker(cluster, podName, "rolling upgrade cannot continue while non-target pod %s is not ready; current target is %s", podName, targetPodName)
		}

		apiClient, err := raftops.NewClusterPodClient(cluster, podName, caCert, m.clientFactory, raftops.ClusterPodClientOptions{})
		if err != nil {
			return resumePodReadyBlocker(cluster, podName, "rolling upgrade cannot continue while non-target pod %s is unavailable: %w", podName, err)
		}

		healthy, err := apiClient.IsHealthy(ctx)
		if err != nil {
			logger.V(1).Info("Non-target pod health check failed during rolling resume validation", "pod", podName, "error", err)
			return resumePodHealthBlocker(cluster, podName, "rolling upgrade cannot continue while non-target pod %s is unhealthy: %w", podName, err)
		}
		if !healthy {
			return resumePodHealthBlocker(cluster, podName, "rolling upgrade cannot continue while non-target pod %s is unhealthy; current target is %s", podName, targetPodName)
		}
	}

	return nil
}

func resumePodReadyBlocker(cluster *openbaov1alpha1.OpenBaoCluster, podName string, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	if timeoutErr := markResumeUpgradeTimeout(cluster, podName); timeoutErr != nil {
		return fmt.Errorf("%v: %w", err, timeoutErr)
	}
	return transientClusterState(upgrade.ReasonPodNotReady, err)
}

func resumePodHealthBlocker(cluster *openbaov1alpha1.OpenBaoCluster, podName string, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	if timeoutErr := failUpgradeIfStartedTimeout(cluster, podHealthTimeout(podName, cluster)); timeoutErr != nil {
		return fmt.Errorf("%v: %w", err, timeoutErr)
	}
	return transientClusterState(upgrade.ReasonHealthCheckFailed, err)
}

func transientClusterState(reason string, err error) error {
	return operatorerrors.WithReason(
		reason,
		operatorerrors.WrapTransientClusterState(err),
	)
}

func transientClusterStatef(reason string, format string, args ...any) error {
	return transientClusterState(reason, fmt.Errorf(format, args...))
}

// checkPodHealth queries each pod's health status and returns counts.
func (m *Manager) checkPodHealth(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	pods []corev1.Pod,
) (clusterHealthCounts, error) {
	caCert, err := raftops.LoadClusterCACert(ctx, m.client, cluster)
	if err != nil {
		return clusterHealthCounts{}, fmt.Errorf("failed to get CA certificate: %w", err)
	}

	counts := clusterHealthCounts{}
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		apiClient, err := raftops.NewClusterPodClient(cluster, pod.Name, caCert, m.clientFactory, raftops.ClusterPodClientOptions{})
		if err != nil {
			logger.V(1).Info("Failed to create client for pod", "pod", pod.Name, "error", err)
			continue
		}

		healthy, err := apiClient.IsHealthy(ctx)
		if err != nil {
			logger.V(1).Info("Health check failed for pod", "pod", pod.Name, "error", err)
			continue
		}
		if healthy {
			counts.healthyPods++
		}

		isLeader, present, err := portopenbao.ParseBoolLabel(pod.Labels, portopenbao.LabelActive)
		if err != nil {
			logger.V(1).Info("Invalid OpenBao leader label value", "pod", pod.Name, "error", err)
			continue
		}

		if !present {
			isLeader, err = apiClient.IsLeader(ctx)
			if err != nil {
				logger.V(1).Info("Leader check failed for pod", "pod", pod.Name, "error", err)
				continue
			}
		}

		if isLeader {
			counts.leaderCount++
			cluster.Status.ActiveLeader = pod.Name
		}
	}

	return counts, nil
}
