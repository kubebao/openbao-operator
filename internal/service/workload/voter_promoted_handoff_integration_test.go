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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestPromotedVoterHandoffWithConfigDriftRealAPI(t *testing.T) {
	environment := &envtest.Environment{}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	live, err := client.NewWithWatch(config, client.Options{Scheme: testScheme})
	require.NoError(t, err)
	ctx, logger := t.Context(), logr.Discard()
	const namespace = "promoted-handoff"
	require.NoError(t, live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	require.NoError(t, live.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: namespace}}))
	cluster := newMinimalCluster("bao", namespace)
	cluster.Generation = 1
	cluster.Spec.TLS.Enabled = false
	cluster.Spec.Upgrade = &api.UpgradeConfig{Strategy: api.UpdateStrategyBlueGreen}
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
	cluster.Status.BlueGreen = &api.BlueGreenStatus{Phase: api.PhaseIdle, BlueRevision: "promoted"}
	m := NewManager(live, testScheme, "")
	spec := StatefulSetSpec{Name: "bao-promoted", Revision: "promoted", Pool: constants.LabelValueOpenBaoWorkloadPoolVoter, Replicas: 3}
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "before-strategy-handoff", spec))
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: namespace, Name: spec.Name}
	require.NoError(t, live.Get(ctx, key, sts))
	uid := sts.UID
	sts.Status = appsv1.StatefulSetStatus{ObservedGeneration: sts.Generation, Replicas: 3, CurrentReplicas: 3, ReadyReplicas: 3,
		CurrentRevision: "promoted-running", UpdateRevision: "promoted-template"}
	require.NoError(t, live.Status().Update(ctx, sts))
	podUIDs := map[string]types.UID{}
	for ordinal := 0; ordinal < 3; ordinal++ {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", sts.Name, ordinal), Namespace: namespace,
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: sts.Status.CurrentRevision},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}}}
		require.NoError(t, live.Create(ctx, pod))
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		require.NoError(t, live.Status().Update(ctx, pod))
		podUIDs[pod.Name] = pod.UID
	}
	cluster.Spec.Upgrade.Strategy = api.UpdateStrategyRollingUpdate
	cluster.Status.AcceptedUpgradeStrategy = api.UpdateStrategyRollingUpdate
	cluster.Generation++
	publishedTemplate := sts.Spec.Template.DeepCopy()
	for _, kind := range []string{"ConfigMap", "StatefulSet"} {
		blocked := interceptor.NewClient(live, interceptor.Funcs{Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			if object, ok := obj.(client.Object); ok && object.GetObjectKind().GroupVersionKind().Kind == kind {
				return apierrors.NewServiceUnavailable("injected handoff publication failure")
			}
			return c.Apply(ctx, obj, opts...)
		}})
		err = NewManager(blocked, testScheme, "").WithReader(live).EnsureStatefulSet(ctx, logger, cluster, "after-strategy-handoff", spec)
		require.True(t, apierrors.IsServiceUnavailable(err), "publication of %s must fail after locking the strategy: %v", kind, err)
		require.NoError(t, live.Get(ctx, key, sts))
		require.Equal(t, uid, sts.UID)
		require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
		require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
		require.Contains(t, sts.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate)
		require.Equal(t, publishedTemplate, &sts.Spec.Template, "failed publication retains the existing Pod template")
	}
	require.NoError(t, m.EnsureStatefulSet(ctx, logger, cluster, "after-strategy-handoff", spec))
	require.NoError(t, live.Get(ctx, key, sts))
	require.Equal(t, uid, sts.UID)
	require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	require.Contains(t, sts.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate)
	require.NoError(t, NewManager(live, testScheme, "").EnsureStatefulSet(ctx, logger, cluster, "after-strategy-handoff", spec))
	require.NoError(t, live.Get(ctx, key, sts))
	require.EqualValues(t, 3, *sts.Spec.UpdateStrategy.RollingUpdate.Partition, "SSA and controller reconstruction preserve the frozen promoted voters")
	for name, originalUID := range podUIDs {
		pod := &corev1.Pod{}
		require.NoError(t, live.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod))
		require.Equal(t, originalUID, pod.UID)
	}
}
