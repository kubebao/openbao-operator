package workload

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestDecideStatefulSetUpdateStrategy(t *testing.T) {
	const previousVersion = "2.5.5"
	rolling := func(partition int32) appsv1.StatefulSetUpdateStrategy {
		return appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(partition)},
		}
	}
	onDelete := appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	lock := statefulSetStrategyDecision{action: lockStatefulSetPartition, strategy: rolling(3)}
	tests := []struct {
		name   string
		pool   string
		mutate func(*api.OpenBaoCluster, *appsv1.StatefulSet)
		want   statefulSetStrategyDecision
	}{
		{name: "preserve idle rolling partition"},
		{
			name: "lock pending version upgrade",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
			},
			want: lock,
		},
		{
			name: "lock upgrade before restoring OnDelete strategy",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				s.Spec.UpdateStrategy = onDelete
			},
			want: lock,
		},
		{
			name: "pending upgrade already locked",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				s.Spec.UpdateStrategy = rolling(3)
			},
		},
		{
			name: "pending upgrade without rolling payload",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				s.Spec.UpdateStrategy.RollingUpdate = nil
			},
			want: lock,
		},
		{
			name: "pending upgrade without partition",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				s.Spec.UpdateStrategy.RollingUpdate.Partition = nil
			},
			want: lock,
		},
		{
			name: "active upgrade retains AdminOps partition",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				c.Status.Upgrade = &api.UpgradeProgress{CurrentPartition: 1}
			},
		},
		{
			name: "uninitialized cluster does not prelock",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.Initialized = false
				c.Status.CurrentVersion = previousVersion
			},
		},
		{
			name: "unknown current version does not prelock",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.CurrentVersion = ""
			},
		},
		{
			name: "read replicas do not use voter strategy decisions",
			pool: constants.LabelValueOpenBaoWorkloadPoolReadReplica,
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.CurrentVersion = previousVersion
				s.Spec.UpdateStrategy = onDelete
			},
		},
		{
			name: "unaccepted BlueGreen request preserves rolling strategy",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Spec.Upgrade = &api.UpgradeConfig{Strategy: api.UpdateStrategyBlueGreen}
				c.Status.AcceptedUpgradeStrategy = api.UpdateStrategyRollingUpdate
			},
		},
		{
			name: "accepted BlueGreen strategy changes to OnDelete",
			mutate: func(c *api.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
				c.Status.CurrentVersion = previousVersion
			},
			want: statefulSetStrategyDecision{action: transitionStatefulSetStrategy, strategy: onDelete},
		},
		{
			name: "preserve idle OnDelete strategy",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
				s.Spec.UpdateStrategy = onDelete
			},
		},
		{
			name: "OnDelete transition clears rolling payload",
			mutate: func(c *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				c.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
				s.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
			},
			want: statefulSetStrategyDecision{action: transitionStatefulSetStrategy, strategy: onDelete},
		},
		{
			name: "restore rolling strategy with settled template",
			mutate: func(_ *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Spec.UpdateStrategy = onDelete
				s.Status.CurrentRevision = "same"
				s.Status.UpdateRevision = "same"
			},
			want: statefulSetStrategyDecision{action: transitionStatefulSetStrategy, strategy: rolling(0)},
		},
		{
			name: "post-promotion template retains existing pods",
			mutate: func(_ *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Spec.UpdateStrategy = onDelete
				s.Status.CurrentRevision = "blue"
				s.Status.UpdateRevision = "post-promotion"
			},
			want: statefulSetStrategyDecision{action: transitionStatefulSetStrategy, strategy: rolling(3)},
		},
		{
			name: "missing revision does not imply a pending rollout",
			mutate: func(_ *api.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Spec.UpdateStrategy = onDelete
				s.Status.UpdateRevision = "post-promotion"
			},
			want: statefulSetStrategyDecision{action: transitionStatefulSetStrategy, strategy: rolling(0)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := workloadOwnershipCluster()
			cluster.Spec.Replicas = 3
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			existing := workloadOwnershipStatefulSet(cluster.Name, cluster.Namespace, workloadOwnershipRef(cluster), 3)
			existing.Spec.UpdateStrategy = rolling(1)
			pool := constants.LabelValueOpenBaoWorkloadPoolVoter
			if tt.pool != "" {
				pool = tt.pool
			}
			if tt.mutate != nil {
				tt.mutate(cluster, existing)
			}
			beforeCluster, beforeExisting := cluster.DeepCopy(), existing.DeepCopy()
			require.Equal(t, tt.want, decideStatefulSetUpdateStrategy(cluster, pool, existing))
			require.Equal(t, beforeCluster, cluster, "decision must not mutate the cluster")
			require.Equal(t, beforeExisting, existing, "decision must not mutate the StatefulSet")
		})
	}
	// Creation has no existing strategy to patch.
	require.Equal(t, statefulSetStrategyDecision{}, decideStatefulSetUpdateStrategy(
		workloadOwnershipCluster(), constants.LabelValueOpenBaoWorkloadPoolVoter, nil))
}
