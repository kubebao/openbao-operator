package workload

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

func TestVoterTemplateDefersScaleOnlyWhileRepairActive(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, replicas := range []int32{1, 5} {
			t.Run(fmt.Sprintf("repair=%t/replicas=%d", active, replicas), func(t *testing.T) {
				m, cluster, sts, _ := voterTemplateFixture(t)
				sts.Status.CurrentRevision = sts.Status.UpdateRevision
				require.NoError(t, m.client.Status().Update(t.Context(), sts))
				desired := sts.DeepCopy()
				spec := StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}
				require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, desired))
				require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
				sts.Annotations[constants.AnnotationVoterRolloutTemplate] = desired.Annotations[constants.AnnotationVoterRolloutTemplate]
				sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(0))}}
				if active {
					sts.Annotations[constants.AnnotationVoterTemplateRepair] = desired.Annotations[constants.AnnotationVoterRolloutTemplate]
					sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
				}
				require.NoError(t, m.client.Update(t.Context(), sts))
				if active {
					sts.Status.CurrentRevision = "before-scale-request"
					require.NoError(t, m.client.Status().Update(t.Context(), sts))
				}
				cluster.Spec.Replicas = replicas
				cluster.Generation++
				desired = sts.DeepCopy()
				err := m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, desired)
				if active {
					require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending, "a matching fingerprint must not bypass repair scale deferral")
				} else {
					require.NoError(t, err, "a settled workload can accept a scale-only request")
					require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
					require.EqualValues(t, 0, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "scaling a settled workload does not lock a new template")
				}
			})
		}
	}
}
