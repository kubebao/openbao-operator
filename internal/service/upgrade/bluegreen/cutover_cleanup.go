package bluegreen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func (m *Manager) markBlueGreenStepDown(cluster *openbaov1alpha1.OpenBaoCluster) *upgrade.Metrics {
	metrics := upgrade.NewMetrics(cluster.Namespace, cluster.Name)
	_, counted := core.MarkUpgradeMetricsStepDownCounted(cluster.Namespace, cluster.Name, time.Now())
	if counted {
		metrics.IncrementStepDownTotal()
	}
	return metrics
}

func (m *Manager) ensureGreenReadyForBlueDemotion(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) ([]podSnapshot, error) {
	greenRevision := cluster.Status.BlueGreen.GreenRevision
	if greenRevision == "" {
		return nil, fmt.Errorf("green revision is empty in DemotingBlue phase")
	}

	greenPods, err := m.getPodsByRevision(ctx, cluster, greenRevision)
	if err != nil {
		return nil, fmt.Errorf("failed to get Green pods: %w", err)
	}
	greenSnapshots, err := podSnapshotsFromPods(greenPods)
	if err != nil {
		return nil, err
	}

	ok, message := demotionPreconditionsSatisfied(greenSnapshots, int(upgrade.BlueGreenTargetReplicas(cluster)))
	if !ok {
		logger.Info(message)
		return nil, nil
	}

	return greenSnapshots, nil
}

func (m *Manager) waitForGreenLeaderAfterBlueDemotion(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (phaseOutcome, bool, error) {
	greenPods, err := m.getPodsByRevision(ctx, cluster, cluster.Status.BlueGreen.GreenRevision)
	if err != nil {
		return phaseOutcome{}, true, fmt.Errorf("failed to get Green pods after demotion: %w", err)
	}

	leaderPod, source, ok := m.clusterOps.FindLeaderPod(ctx, logger, cluster, greenPods)
	if !ok {
		logger.Info("Green leader not yet elected after demotion, waiting...")
		return requeueAfterOutcome(constants.RequeueShort), true, nil
	}

	logger.Info("Green leader confirmed after demotion", "pod", leaderPod, "source", source)
	return phaseOutcome{}, false, nil
}

func (m *Manager) ensureGreenReadyForBlueCleanup(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (phaseOutcome, bool, error) {
	greenRevision := cluster.Status.BlueGreen.GreenRevision
	if greenRevision == "" {
		return phaseOutcome{}, true, fmt.Errorf("green revision is empty in Cleanup phase")
	}

	greenPods, err := m.getPodsByRevision(ctx, cluster, greenRevision)
	if err != nil {
		return phaseOutcome{}, true, fmt.Errorf("failed to get Green pods: %w", err)
	}
	greenSnapshots, err := podSnapshotsFromPods(greenPods)
	if err != nil {
		return phaseOutcome{}, true, err
	}

	leaderOK := leaderObserved(greenSnapshots)
	if !leaderOK {
		if _, source, ok := m.clusterOps.FindLeaderPod(ctx, logger, cluster, greenPods); ok {
			leaderOK = true
			logger.V(1).Info("Green leader observed via API fallback", "source", source)
		}
	}

	ok, message := cleanupPreconditionsSatisfied(greenSnapshots, int(upgrade.BlueGreenTargetReplicas(cluster)), leaderOK)
	if !ok {
		logger.Info(message)
		return requeueAfterOutcome(constants.RequeueShort), true, nil
	}

	return phaseOutcome{}, false, nil
}

func (m *Manager) ensureBlueStatefulSetDeleted(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (phaseOutcome, bool, error) {
	status := cluster.Status.BlueGreen
	if status == nil || strings.TrimSpace(status.GreenRevision) == "" ||
		strings.TrimSpace(status.BlueRevision) == strings.TrimSpace(status.GreenRevision) {
		return phaseOutcome{}, true, fmt.Errorf("distinct Blue and Green revisions are required for Blue cleanup")
	}
	if err := resourceownership.RequireOwnerUID(cluster); err != nil {
		return phaseOutcome{}, true, fmt.Errorf("delete Blue StatefulSet: %w", err)
	}
	blueRevision := strings.TrimSpace(status.BlueRevision)
	blueStatefulSetName := upgrade.StableVoterStatefulSetName(cluster)
	if blueStatefulSetName == resourceidentity.ReadReplicaStatefulSetName(cluster) {
		return phaseOutcome{}, true, fmt.Errorf("cannot retire the read-replica workload during Blue cleanup")
	}
	reader := m.reader
	if reader == nil {
		reader = m.client
	}
	blueStatefulSet := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      blueStatefulSetName,
	}, blueStatefulSet); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Blue StatefulSet already deleted", "blueRevision", blueRevision)
			return phaseOutcome{}, false, nil
		}
		return phaseOutcome{}, true, fmt.Errorf("failed to get Blue StatefulSet: %w", err)
	}

	if err := resourceownership.RequireManagedControllerOwnerProof("delete Blue StatefulSet", blueStatefulSet, cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")); err != nil {
		return phaseOutcome{}, true, err
	}
	if blueStatefulSet.Spec.Template.Labels[constants.LabelOpenBaoRevision] != blueRevision {
		return phaseOutcome{}, true, fmt.Errorf("StatefulSet %s does not identify the retired Blue revision", blueStatefulSetName)
	}
	// Older status can identify the original unrevisioned workload by owner
	// provenance alone. When recorded, also require its captured controller revision.
	if blueRevision == "" && status.BlueControllerRevision != "" &&
		blueStatefulSet.Status.CurrentRevision != status.BlueControllerRevision {
		return phaseOutcome{}, true, fmt.Errorf("StatefulSet %s does not match the captured Blue controller revision", blueStatefulSetName)
	}
	if blueStatefulSet.DeletionTimestamp == nil {
		err := m.client.Delete(ctx, blueStatefulSet, client.Preconditions{
			UID: &blueStatefulSet.UID, ResourceVersion: &blueStatefulSet.ResourceVersion,
		})
		if client.IgnoreNotFound(err) != nil {
			return phaseOutcome{}, true, fmt.Errorf("failed to delete Blue StatefulSet: %w", err)
		}
	}

	logger.Info("Waiting for Blue StatefulSet deletion", "blueRevision", blueRevision)
	return requeueAfterOutcome(constants.RequeueShort), true, nil
}

func (m *Manager) ensureBluePodsTerminated(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (phaseOutcome, bool, error) {
	bluePods, err := m.getPodsByRevision(ctx, cluster, cluster.Status.BlueGreen.BlueRevision)
	if err != nil {
		return phaseOutcome{}, true, fmt.Errorf("failed to check Blue pods: %w", err)
	}

	activeBluePods := countActivePods(bluePods)
	if activeBluePods > 0 {
		logger.Info("Blue pods still exist, waiting for termination", "count", activeBluePods)
		return requeueAfterOutcome(constants.RequeueShort), true, nil
	}

	return phaseOutcome{}, false, nil
}
