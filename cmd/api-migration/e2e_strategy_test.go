//go:build apimigration_e2e

package main

import (
	"encoding/json"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

func (f *e2eFixture) voterStatefulSet() *appsv1.StatefulSet {
	f.t.Helper()
	set := &appsv1.StatefulSet{}
	require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, "get", "statefulset", f.voterSet, "-n", e2eDataNS, "-o", "json")), set))
	return set
}

func (f *e2eFixture) voterPodUIDs() map[string]string {
	f.t.Helper()
	identities := map[string]string{}
	pods := f.getJSON("get", "pods", "-n", e2eDataNS, "-l", "openbao.org/cluster="+e2eCluster)
	for _, raw := range pods["items"].([]any) {
		metadata := raw.(map[string]any)["metadata"].(map[string]any)
		identities[metadata["name"].(string)] = metadata["uid"].(string)
	}
	require.Len(f.t, identities, f.shape.replicas)
	return identities
}

// A healthy BlueGreen workload retains old Pod revisions after a template
// change. A transition to RollingUpdate can deliberately hold them behind a
// partition. Both must survive forward migration, rollback, and commit.
func (f *e2eFixture) migrateBlueGreen(partitioned bool) {
	f.t.Helper()
	pods := f.voterPodUIDs()
	f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eCluster, "-n", e2eDataNS,
		"--type=merge", "-p", `{"spec":{"podMetadata":{"annotations":{"migration.example.org/revision":"held"}}}}`)
	f.eventually("healthy OnDelete Pods on the old revision", 5*time.Minute, func() bool {
		set := f.voterStatefulSet()
		return clusterReady(f.cluster(primitivesSourceGroup)) && set.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType &&
			set.Status.CurrentRevision != set.Status.UpdateRevision && set.Status.UpdatedReplicas == 0
	})
	if partitioned {
		f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eCluster, "-n", e2eDataNS,
			"--type=merge", "-p", `{"spec":{"upgrade":{"strategy":"RollingUpdate"}}}`)
		f.eventually("the idle rolling partition to retain every old Pod", 5*time.Minute, func() bool {
			set := f.voterStatefulSet()
			rolling := set.Spec.UpdateStrategy.RollingUpdate
			return clusterReady(f.cluster(primitivesSourceGroup)) && rolling != nil && rolling.Partition != nil &&
				*rolling.Partition == int32(f.shape.replicas) && set.Status.UpdatedReplicas == 0
		})
	}
	assertRetained := func() {
		f.t.Helper()
		require.Equal(f.t, pods, f.voterPodUIDs(), "migration must retain every voter Pod")
		set := f.voterStatefulSet()
		require.Zero(f.t, set.Status.UpdatedReplicas, "the held template must not replace running Pods")
		if partitioned {
			require.Equal(f.t, int32(f.shape.replicas), *set.Spec.UpdateStrategy.RollingUpdate.Partition)
		} else {
			require.Equal(f.t, appsv1.OnDeleteStatefulSetStrategyType, set.Spec.UpdateStrategy.Type)
		}
	}
	state := f.newState("rollback")
	f.plan(state)
	f.migrate(state)
	assertRetained()
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "rollback")
	assertRetained()
	f.migrateAndCommit(f.newState("commit"))
	assertRetained()
}
