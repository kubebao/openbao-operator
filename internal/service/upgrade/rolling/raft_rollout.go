package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func (m *Manager) checkRaftVoterRollout(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, target string, replacing bool) error {
	sts, replicas, err := m.getVoterRolloutStatefulSet(ctx, cluster)
	if err != nil {
		return err
	}
	if replicas == 1 {
		return nil
	}
	if m.voterRolloutGuard == nil {
		return fmt.Errorf("%w: authenticated Raft voter rollout guard is unavailable", errPartitionRetry)
	}
	if err := m.voterRolloutGuard.CheckVoterRollout(ctx, logger, cluster, sts, target, replacing); err != nil {
		return fmt.Errorf("%w: %w", errPartitionRetry, err)
	}
	return nil
}

func (m *Manager) getVoterRolloutStatefulSet(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (*appsv1.StatefulSet, int32, error) {
	sts := &appsv1.StatefulSet{}
	if err := m.jobReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: upgrade.StableVoterStatefulSetName(cluster)}, sts); err != nil {
		return nil, 0, fmt.Errorf("%w: observe voter rollout StatefulSet: %w", errPartitionRetry, err)
	}
	replicas, err := portworkload.VoterReplicaCount(sts)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", errPartitionRetry, operatorerrors.WrapTransientKubernetesAPI(err))
	}
	return sts, replicas, nil
}
