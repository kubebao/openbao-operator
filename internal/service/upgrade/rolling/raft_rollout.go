package rolling

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func (m *Manager) checkRaftVoterRollout(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, target string, replacing bool) error {
	if cluster.Spec.Replicas <= 1 {
		return nil
	}
	sts := &appsv1.StatefulSet{}
	if err := m.jobReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: upgrade.StableVoterStatefulSetName(cluster)}, sts); err != nil {
		return fmt.Errorf("%w: observe voter rollout StatefulSet: %w", errPartitionRetry, err)
	}
	if m.voterRolloutGuard == nil {
		return fmt.Errorf("%w: authenticated Raft voter rollout guard is unavailable", errPartitionRetry)
	}
	if err := m.voterRolloutGuard.CheckVoterRollout(ctx, logger, cluster, sts, target, replacing); err != nil {
		return fmt.Errorf("%w: %w", errPartitionRetry, err)
	}
	return nil
}
