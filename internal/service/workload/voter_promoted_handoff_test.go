package workload

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

func TestPromotedVoterStrategyHandoffWithRenderedTemplateDrift(t *testing.T) {
	for _, scenario := range []string{"idle", "no promotion", "wrong promotion", "active phase", "unobserved", "mixed revisions", "unready", "scaling", "scale", "version"} {
		t.Run(scenario, func(t *testing.T) {
			_, cluster, sts, _ := voterTemplateFixture(t)
			cluster.Status.AcceptedUpgradeStrategy = api.UpdateStrategyRollingUpdate
			cluster.Status.BlueGreen = &api.BlueGreenStatus{Phase: api.PhaseIdle, BlueRevision: "promoted"}
			sts.Name = cluster.Name + "-promoted"
			sts.Annotations = map[string]string{constants.AnnotationClusterGeneration: "1"}
			sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
			sts.Spec.Template.Annotations = map[string]string{constants.AnnotationConfigHash: "before-handoff"}
			sts.Status.Replicas, sts.Status.CurrentReplicas, sts.Status.ReadyReplicas = 3, 3, 3
			objects := []client.Object{cluster, sts}
			for ordinal := 0; ordinal < 3; ordinal++ {
				name := fmt.Sprintf("%s-%d", sts.Name, ordinal)
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace, UID: types.UID(name),
					Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: sts.Status.CurrentRevision},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
			}
			switch scenario {
			case "no promotion":
				cluster.Status.BlueGreen = nil
			case "wrong promotion":
				cluster.Status.BlueGreen.BlueRevision = "another"
			case "active phase":
				cluster.Status.BlueGreen.Phase = api.PhaseSyncing
			case "unobserved":
				sts.Status.ObservedGeneration = 0
			case "mixed revisions":
				sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas = 2, 1
			case "unready":
				sts.Status.ReadyReplicas = 2
			case "scaling":
				sts.Status.Replicas = 2
			case "scale":
				cluster.Spec.Replicas = 1
			case "version":
				cluster.Spec.Version = "2.7.3"
			}
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objects...).Build()
			m := NewManager(c, testScheme, "")
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			desired := sts.DeepCopy()
			desired.Spec.Template.Annotations[constants.AnnotationConfigHash] = "after-handoff"
			err := m.prepareVoterTemplateRollout(t.Context(), cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter}, sts, desired)
			if scenario != "idle" {
				require.ErrorIs(t, err, portworkload.ErrVoterTemplateRolloutPending)
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
				require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
				return
			}
			require.NoError(t, err)
			require.Contains(t, desired.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate)
			require.NoError(t, m.reconcileStatefulSetUpdateStrategy(t.Context(), logr.Discard(), cluster, constants.LabelValueOpenBaoWorkloadPoolVoter, sts))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
			require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
			sts.Spec.Template = desired.Spec.Template
			sts.Annotations = desired.Annotations
			require.NoError(t, c.Update(t.Context(), sts))
			result, err := m.ReconcileVoterRollout(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Zero(t, result.RequeueAfter, "the strategy change does not begin a voter replacement")
			for _, object := range objects[2:] {
				pod := &corev1.Pod{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(object), pod))
				require.Equal(t, object.GetUID(), pod.UID)
			}
		})
	}
}
