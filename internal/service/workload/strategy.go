package workload

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

type statefulSetStrategyAction uint8

const (
	preserveStatefulSetStrategy statefulSetStrategyAction = iota
	transitionStatefulSetStrategy
	lockStatefulSetPartition
)

type statefulSetStrategyDecision struct {
	action   statefulSetStrategyAction
	strategy appsv1.StatefulSetUpdateStrategy
}

// decideStatefulSetUpdateStrategy selects workload-owned mutations. AdminOps
// advances the rolling partition after it records an upgrade in cluster status.
func decideStatefulSetUpdateStrategy(cluster *openbaov1alpha1.OpenBaoCluster, pool string, existing *appsv1.StatefulSet) statefulSetStrategyDecision {
	if existing == nil || pool != constants.LabelValueOpenBaoWorkloadPoolVoter {
		return statefulSetStrategyDecision{}
	}

	rolling := portworkload.EffectiveStrategy(cluster) == openbaov1alpha1.UpdateStrategyRollingUpdate
	pendingVersionUpgrade := rolling && cluster.Status.Initialized && cluster.Status.Upgrade == nil &&
		cluster.Status.CurrentVersion != "" && cluster.Status.CurrentVersion != cluster.Spec.Version
	if pendingVersionUpgrade {
		partition := cluster.Spec.Replicas
		if current := existing.Spec.UpdateStrategy.RollingUpdate; current != nil &&
			current.Partition != nil && *current.Partition == partition {
			return statefulSetStrategyDecision{}
		}
		return statefulSetStrategyDecision{
			action: lockStatefulSetPartition,
			strategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
			},
		}
	}

	if !rolling {
		if existing.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType &&
			existing.Spec.UpdateStrategy.RollingUpdate == nil {
			return statefulSetStrategyDecision{}
		}
		return statefulSetStrategyDecision{
			action:   transitionStatefulSetStrategy,
			strategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		}
	}
	if existing.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		return statefulSetStrategyDecision{}
	}

	partition := int32(0)
	if existing.Status.CurrentRevision != "" && existing.Status.UpdateRevision != "" &&
		existing.Status.CurrentRevision != existing.Status.UpdateRevision {
		// A promoted Blue workload can have a newer template than its healthy
		// OnDelete pods. Keep those pods until the next orchestrated upgrade.
		partition = cluster.Spec.Replicas
	}
	return statefulSetStrategyDecision{
		action: transitionStatefulSetStrategy,
		strategy: appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
		},
	}
}

func (m *Manager) reconcileStatefulSetUpdateStrategy(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, pool string, existing *appsv1.StatefulSet) error {
	decision := decideStatefulSetUpdateStrategy(cluster, pool, existing)
	if decision.action == preserveStatefulSetStrategy {
		return nil
	}
	if err := resourceownership.RequireOwnerProof("change workload StatefulSet update strategy", existing, cluster); err != nil {
		return err
	}

	operation := fmt.Sprintf("change update strategy on StatefulSet %s/%s", cluster.Namespace, existing.Name)
	if decision.action == lockStatefulSetPartition {
		operation = "lock StatefulSet partition for upgrade"
		logger.Info("Locking StatefulSet rolling partition prior to version upgrade",
			"statefulset", existing.Name, "partition", *decision.strategy.RollingUpdate.Partition,
			"fromVersion", cluster.Status.CurrentVersion, "toVersion", cluster.Spec.Version)
	} else {
		logger.Info("Changing idle StatefulSet update strategy",
			"statefulset", existing.Name, "strategy", decision.strategy.Type)
	}

	// Patch the discriminator and payload together. Typed SSA cannot clear the
	// API-defaulted rollingUpdate payload while switching to OnDelete.
	patched := existing.DeepCopy()
	patched.Spec.UpdateStrategy = decision.strategy
	// Re-observe ownership and strategy after a replacement or concurrent write.
	if err := m.client.Patch(ctx, patched, client.MergeFromWithOptions(existing, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("failed to %s: %w", operation, err)
	}
	return nil
}
