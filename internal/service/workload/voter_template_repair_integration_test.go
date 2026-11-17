//go:build integration

package workload

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

func TestVoterTemplateCorrectionRealAPI(t *testing.T) {
	for _, name := range []string{"failed", "deferred", "reverted"} {
		t.Run(name, func(t *testing.T) {
			environment := &envtest.Environment{}
			config, err := environment.Start()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, environment.Stop()) })
			live, err := client.NewWithWatch(config, client.Options{Scheme: testScheme})
			require.NoError(t, err)
			ctx, logger := t.Context(), logr.Discard()
			require.NoError(t, live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
			require.NoError(t, live.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: name}}))
			_, _, _, observer := voterTemplateFixture(t)
			cluster := newMinimalCluster("bao", name)
			cluster.Spec.TLS.Enabled = false
			cluster.Generation = 1
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			m := NewManager(live, testScheme, "").WithRaftMembershipReader(observer)
			spec := StatefulSetSpec{Name: cluster.Name, Pool: constants.LabelValueOpenBaoWorkloadPoolVoter, Replicas: 3}
			key := client.ObjectKey{Namespace: name, Name: cluster.Name}
			require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "original", spec))
			sts := &appsv1.StatefulSet{}
			require.NoError(t, live.Get(ctx, key, sts))
			sts.Status = appsv1.StatefulSetStatus{Replicas: 3, ObservedGeneration: sts.Generation, CurrentRevision: "original", UpdateRevision: "original"}
			require.NoError(t, live.Status().Update(ctx, sts))
			cluster.Spec.Probes = &openbaov1alpha1.ProbeConfig{Startup: &openbaov1alpha1.StartupProbeConfig{FailureThreshold: 61}}
			cluster.Generation++
			require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "first-template", spec))
			require.NoError(t, live.Get(ctx, key, sts))
			sts.Spec.UpdateStrategy.RollingUpdate.Partition = ptr.To(int32(2))
			if name == "deferred" {
				sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
				sts.Annotations[constants.AnnotationVoterRolloutTemplate] = deferredVoterTemplate + sts.Annotations[constants.AnnotationVoterRolloutTemplate]
			}
			require.NoError(t, live.Update(ctx, sts))
			sts.Status = appsv1.StatefulSetStatus{Replicas: 3, ObservedGeneration: sts.Generation, CurrentRevision: "original", UpdateRevision: "first"}
			require.NoError(t, live.Status().Update(ctx, sts))
			pods := map[string]*corev1.Pod{}
			for ordinal := 0; ordinal < 3; ordinal++ {
				podName, revision := fmt.Sprintf("bao-%d", ordinal), "original"
				if ordinal == 2 {
					revision = "first"
				}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: name,
					Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: revision},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}}}
				require.NoError(t, live.Create(ctx, pod))
				ready := corev1.ConditionTrue
				if ordinal == 2 && name != "deferred" {
					ready = corev1.ConditionFalse
				}
				pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: ready, LastTransitionTime: metav1.Now()}}
				require.NoError(t, live.Status().Update(ctx, pod))
				pods[podName] = pod
			}
			cluster.Spec.Probes.Startup.FailureThreshold = 62
			cluster.Generation++
			if name == "reverted" {
				published := sts.Annotations[constants.AnnotationVoterRolloutTemplate]
				blocked := interceptor.NewClient(live, interceptor.Funcs{Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
					if object, ok := obj.(client.Object); ok && object.GetObjectKind().GroupVersionKind().Kind == "ConfigMap" {
						return apierrors.NewServiceUnavailable("injected configuration publication failure")
					}
					return c.Apply(ctx, obj, opts...)
				}})
				err = NewManager(blocked, testScheme, "").WithReader(live).EnsureStatefulSet(ctx, logger, cluster, "unpublished", spec)
				require.True(t, apierrors.IsServiceUnavailable(err), "publication must fail after installing the repair hold: %v", err)
				require.NoError(t, live.Get(ctx, key, sts))
				require.EqualValues(t, 61, sts.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold)
				require.NotEmpty(t, sts.Annotations[constants.AnnotationVoterTemplateRepair])
				require.NotEqual(t, published, sts.Annotations[constants.AnnotationVoterTemplateRepair])
				sts.Status.ObservedGeneration = sts.Generation
				require.NoError(t, live.Status().Update(ctx, sts))
				pods["bao-2"].Status.Conditions[0].Status = corev1.ConditionTrue
				require.NoError(t, live.Status().Update(ctx, pods["bao-2"]))
				cluster.Spec.Probes.Startup.FailureThreshold = 61
				cluster.Generation++
				require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "first-template", spec))
				require.NoError(t, live.Get(ctx, key, sts))
				require.Equal(t, published, sts.Annotations[constants.AnnotationVoterRolloutTemplate])
				require.Equal(t, published, sts.Annotations[constants.AnnotationVoterTemplateRepair])
				require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
				sts.Status.ObservedGeneration = sts.Generation
				require.NoError(t, live.Status().Update(ctx, sts))
				_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
				require.NoError(t, err)
				peer := observer.state.Servers["bao-2"]
				peer.LastContact = freshVoterContact
				observer.state.Servers["bao-2"] = peer
				_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
				require.NoError(t, err)
				require.True(t, apierrors.IsNotFound(live.Get(ctx, client.ObjectKeyFromObject(pods["bao-1"]), &corev1.Pod{})))
				require.NoError(t, live.Get(ctx, client.ObjectKeyFromObject(pods["bao-2"]), &corev1.Pod{}))
				return
			}
			require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "corrected-template", spec))
			require.NoError(t, live.Get(ctx, key, sts))
			require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
			require.EqualValues(t, 62, sts.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold)
			require.Equal(t, sts.Annotations[constants.AnnotationVoterRolloutTemplate], sts.Annotations[constants.AnnotationVoterTemplateRepair])
			require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "corrected-template", spec))
			require.NoError(t, live.Get(ctx, key, sts))
			require.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type, "SSA preserves the independent repair hold")
			cluster.Spec.Replicas, spec.Replicas = 5, 5
			cluster.Generation++
			require.ErrorIs(t, m.EnsureStatefulSet(ctx, logger, cluster, "corrected-template", spec), portworkload.ErrVoterTemplateRolloutPending)
			require.NoError(t, live.Get(ctx, key, sts))
			require.EqualValues(t, 3, *sts.Spec.Replicas, "a scale-only request waits while repair advances with the published count")
			sts.Status.ObservedGeneration, sts.Status.UpdateRevision = sts.Generation, correctedVoterRevision
			require.NoError(t, live.Status().Update(ctx, sts))
			_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
			require.NoError(t, err)
			peer := observer.state.Servers["bao-1"]
			peer.LastContact = freshVoterContact
			observer.state.Servers["bao-1"] = peer
			_, err = m.ReconcileVoterRollout(ctx, logger, cluster)
			require.NoError(t, err)
			require.True(t, apierrors.IsNotFound(live.Get(ctx, client.ObjectKeyFromObject(pods["bao-2"]), &corev1.Pod{})))
			unchanged := &corev1.Pod{}
			require.NoError(t, live.Get(ctx, client.ObjectKeyFromObject(pods["bao-1"]), unchanged))
			require.Equal(t, pods["bao-1"].UID, unchanged.UID)
			replacement := pods["bao-2"].DeepCopy()
			replacement.UID, replacement.ResourceVersion = "", ""
			replacement.Labels[appsv1.StatefulSetRevisionLabel] = correctedVoterRevision
			replacement.Status = corev1.PodStatus{}
			require.NoError(t, live.Create(ctx, replacement))
			_, err = NewManager(live, testScheme, "").WithRaftMembershipReader(observer).ReconcileVoterRollout(ctx, logger, cluster)
			require.NoError(t, err)
			require.NoError(t, live.Get(ctx, client.ObjectKeyFromObject(replacement), unchanged))
			require.Equal(t, replacement.UID, unchanged.UID, "the desired revision remains intact while starting")
		})
	}
}
