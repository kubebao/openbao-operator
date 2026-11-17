package rolling

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestTargetPodAlreadyRolledOutRequiresObservedTemplate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		observed         int64
		podImage         string
		sourceGeneration string
		templateImage    string
		terminating      bool
		want             bool
	}{
		{name: "unobserved template revision", sourceGeneration: "2", observed: 6, podImage: "openbao:old"},
		{name: "matching revision with wrong image", sourceGeneration: "2", observed: 7, podImage: "openbao:old"},
		{name: "terminating target", sourceGeneration: "2", observed: 7, podImage: "openbao:new", terminating: true},
		{name: "observed matching target", sourceGeneration: "2", observed: 7, podImage: "openbao:new", want: true},
		{name: "old template and Pod before infra applies target", sourceGeneration: "1", observed: 7, templateImage: "openbao:old", podImage: "openbao:old"},
		{name: "missing source generation", observed: 7, podImage: "openbao:new"},
		{name: "verified digest target", sourceGeneration: "2", observed: 7, templateImage: "openbao@sha256:verified", podImage: "openbao@sha256:verified", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "ns", Generation: 2}, Spec: openbaov1alpha1.OpenBaoClusterSpec{Image: "openbao:new"}}
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Generation: 7, Annotations: map[string]string{constants.AnnotationClusterGeneration: tc.sourceGeneration}},
				Spec:   appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: "openbao:new"}}}}},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: tc.observed, UpdateRevision: "same-revision"}}
			if tc.templateImage != "" {
				sts.Spec.Template.Spec.Containers[0].Image = tc.templateImage
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "example-2", Namespace: cluster.Namespace,
				Labels: map[string]string{appsv1.StatefulSetRevisionLabel: "same-revision"}},
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: tc.podImage}}},
				Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			if tc.terminating {
				now := metav1.Now()
				pod.DeletionTimestamp = &now
				pod.Finalizers = []string{"test.example/hold"}
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
			m := &Manager{client: c, reader: c, voterRolloutGuard: renderingRetryGuard{}}
			done, err := m.targetPodAlreadyRolledOut(t.Context(), logr.Discard(), cluster, rolloutTargetPod{Name: pod.Name})
			require.NoError(t, err)
			require.Equal(t, tc.want, done)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pod), &corev1.Pod{}), "observation must not delete a Pod")
		})
	}
}

func TestSetStatefulSetPartitionWaitsForDesiredTemplate(t *testing.T) {
	for _, tc := range []struct {
		name, generation string
		observed         int64
		ready            bool
	}{
		{name: "old template", generation: "1", observed: 7},
		{name: "missing generation", observed: 7},
		{name: "unobserved template", generation: "2", observed: 6},
		{name: "current template", generation: "2", observed: 7, ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "ns", Generation: 2}}
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Generation: 7, Annotations: map[string]string{constants.AnnotationClusterGeneration: tc.generation}}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3)), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(3))}}}, Status: appsv1.StatefulSetStatus{ObservedGeneration: tc.observed}}
			require.NoError(t, corev1.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build()
			m := &Manager{client: c, reader: c, voterRolloutGuard: renderingRetryGuard{}}
			err := m.setStatefulSetPartition(t.Context(), cluster, 2)
			if tc.ready {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
			want := int32(3)
			if tc.ready {
				want = 2
			}
			require.Equal(t, want, *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
		})
	}
}
