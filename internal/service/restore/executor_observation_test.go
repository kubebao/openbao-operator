package restore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func stoppedRestoreExecutor() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: "default", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: "Job", Name: "restore", UID: "job-uid", Controller: new(true),
		}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "restore"}}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "restore", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}}},
	}
}

func TestRestoreExecutorObservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		mutate    func(*corev1.Pod)
		pending   bool
		wantError bool
	}{
		{"terminated", func(*corev1.Pod) {}, false, false},
		{"failed and terminated", func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, false, false},
		{"pending", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, true, false},
		{"running", func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }, true, false},
		{"unknown", func(p *corev1.Pod) { p.Status.Phase = corev1.PodUnknown }, true, false},
		{"missing container status", func(p *corev1.Pod) { p.Status.ContainerStatuses = nil }, true, false},
		{"running despite terminal phase", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}, true, false},
		{"unterminated init sidecar", func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "sidecar", RestartPolicy: new(corev1.ContainerRestartPolicyAlways)}}
		}, true, false},
		{"unterminated ephemeral container", func(p *corev1.Pod) {
			p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}
		}, true, false},
		{"different job UID", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "old-job"; p.Status.Phase = corev1.PodRunning }, false, false},
		{"invalid ownership", func(p *corev1.Pod) { p.OwnerReferences[0].Name = "different" }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			pod := stoppedRestoreExecutor()
			tc.mutate(pod)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			manager := &Manager{reader: c}
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "default", UID: "job-uid"}}
			pending, err := manager.restoreExecutorPending(t.Context(), job)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.pending, pending != "")
		})
	}
}

func TestRestoreExecutorObservationUsesLiveReaderAndAllPages(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "default", UID: "job-uid"}}
	pod := stoppedRestoreExecutor()
	pod.Status.Phase = corev1.PodRunning
	reads := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			reads++
			options := &client.ListOptions{}
			options.ApplyOptions(opts)
			require.Equal(t, job.Namespace, options.Namespace)
			require.Equal(t, int64(500), options.Limit)
			require.Nil(t, options.LabelSelector, "ownership must not depend on labels")
			pods := list.(*corev1.PodList)
			if reads == 1 {
				pods.Continue = "page-two"
			} else {
				require.Equal(t, "page-two", options.Continue)
				pods.Items = []corev1.Pod{*pod}
			}
			return nil
		},
	}).Build()
	m := &Manager{reader: c}
	pending, err := m.restoreExecutorPending(t.Context(), job)
	require.NoError(t, err)
	require.NotEmpty(t, pending)
	require.Equal(t, 2, reads)
	injected := errors.New("API unavailable")
	m.reader = fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return injected
		},
	}).Build()
	_, err = m.restoreExecutorPending(t.Context(), job)
	require.ErrorIs(t, err, injected)
}
