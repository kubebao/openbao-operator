package workload

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

func TestVoterTemplateRepairRebindsAfterInterruptedPublicationAndRevert(t *testing.T) {
	for _, replicas := range []int32{3, 5} {
		t.Run(fmt.Sprint(replicas), func(t *testing.T) {
			m, cluster, sts, observer := voterTemplateFixture(t)
			sts.Status.CurrentRevision = sts.Status.UpdateRevision
			require.NoError(t, m.client.Status().Update(t.Context(), sts))
			spec := StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}
			published := sts.DeepCopy()
			require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, published))
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			fingerprint := published.Annotations[constants.AnnotationVoterRolloutTemplate]
			sts.Annotations[constants.AnnotationVoterRolloutTemplate] = fingerprint
			require.NoError(t, m.client.Update(t.Context(), sts))
			sts.Status.CurrentRevision = "previous-template"
			require.NoError(t, m.client.Status().Update(t.Context(), sts))
			pod := &corev1.Pod{}
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-2"}, pod))
			pod.Status.Conditions[0].Status = corev1.ConditionFalse
			require.NoError(t, m.client.Status().Update(t.Context(), pod))
			correction := sts.DeepCopy()
			correction.Spec.Template.Annotations = map[string]string{"example.com/correction": "never-published"}
			require.NoError(t, m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, correction))
			require.NotEqual(t, fingerprint, sts.Annotations[constants.AnnotationVoterTemplateRepair])
			// Stop before publishing the ConfigMap or StatefulSet, then return to
			// the published template after its slow voter recovers on its own.
			pod.Status.Conditions[0].Status = corev1.ConditionTrue
			require.NoError(t, m.client.Status().Update(t.Context(), pod))
			cluster.Spec.Replicas = replicas
			cluster.Generation++
			reverted := sts.DeepCopy()
			err := m.prepareVoterTemplateRollout(t.Context(), cluster, spec, sts, reverted)
			if replicas == 3 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending)
			}
			require.NoError(t, m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.Equal(t, fingerprint, sts.Annotations[constants.AnnotationVoterRolloutTemplate])
			require.Equal(t, fingerprint, sts.Annotations[constants.AnnotationVoterTemplateRepair], "the unpublished correction cannot strand the published rollout")
			_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			peer := observer.state.Servers["bao-2"]
			peer.LastContact = freshVoterContact
			observer.state.Servers["bao-2"] = peer
			_, err = m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.True(t, apierrors.IsNotFound(m.client.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-1"}, &corev1.Pod{})), "the published rollout can advance again under the Raft guard")
		})
	}
}

func TestVoterRaftGuardUsesPublishedReplicaCount(t *testing.T) {
	for _, actual := range []*int32{nil, ptr.To(int32(0)), ptr.To(int32(1)), ptr.To(int32(3))} {
		name := "unset"
		if actual != nil {
			name = fmt.Sprint(*actual)
		}
		t.Run(name, func(t *testing.T) {
			c, cluster, sts, observer := rolloutGateFixture(t)
			cluster.Spec.Replicas = 1
			sts.Spec.Replicas = actual
			peer := observer.state.Servers["bao-2"]
			peer.Healthy = false
			observer.state.Servers["bao-2"] = peer
			err := checkVoterRollout(t.Context(), logr.Discard(), c, c, observer, cluster, sts, "bao-1", true)
			if actual != nil && *actual == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, portworkload.ErrVoterRolloutWaiting)
			}
		})
	}
}
