package workload

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestCompleteTerminatedPodDeletionBoundaries(t *testing.T) {
	const foreignOwner = "foreign"
	for _, test := range []struct {
		name    string
		change  func(*openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *corev1.Pod)
		allowed bool
	}{
		{name: "missing-pod-uid", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) { p.UID = "" }},
		{name: "missing-pod-version", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) { p.ResourceVersion = "" }},
		{name: "unmanaged-parent", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			delete(s.Labels, constants.LabelAppManagedBy)
		}},
		{name: "failed", allowed: true},
		{name: "succeeded", allowed: true, change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodSucceeded
		}},
		{name: "running", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
		}},
		{name: "pending", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
		}},
		{name: "unknown", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodUnknown
		}},
		{name: "not-deleting", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.DeletionTimestamp = nil
		}},
		{name: "finalizer", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Finalizers = []string{"test.openbao.org/hold"}
		}},
		{name: "active-container", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.ContainerStatuses = activeCleanupContainer()
		}},
		{name: "active-sidecar", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.InitContainerStatuses = activeCleanupContainer()
		}},
		{name: "active-ephemeral", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Status.EphemeralContainerStatuses = activeCleanupContainer()
		}},
		{name: "unmanaged", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			delete(p.Labels, constants.LabelAppManagedBy)
		}},
		{name: "helper-pod", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Labels[constants.LabelOpenBaoComponent] = "backup"
		}},
		{name: "foreign-cluster", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.Labels[constants.LabelOpenBaoCluster] = foreignOwner
		}},
		{name: "missing-owner", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) { p.OwnerReferences = nil }},
		{name: "owner-uid", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].UID = foreignOwner
		}},
		{name: "owner-kind", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].Kind = "Job"
		}},
		{name: "owner-api", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].APIVersion = "apps/v1beta1"
		}},
		{name: "not-controller", change: func(_ *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, p *corev1.Pod) {
			p.OwnerReferences[0].Controller = nil
		}},
		{name: "missing-provenance", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) { s.Annotations = nil }},
		{name: "foreign-provenance", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.Annotations[constants.AnnotationOpenBaoOwnerUID] = foreignOwner
		}},
		{name: "foreign-sts-owner", change: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet, _ *corev1.Pod) {
			s.OwnerReferences[0].UID = foreignOwner
		}},
		{name: "paused", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) { c.Spec.Paused = true }},
		{name: "cluster-deleting", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) {
			c.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
			c.Finalizers = []string{"test.openbao.org/hold"}
		}},
		{name: "restore-hold", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) {
			c.Annotations = map[string]string{constants.AnnotationRestoreHold: "restore"}
		}},
		{name: "no-profile", change: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet, _ *corev1.Pod) { c.Spec.Profile = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster, sts, pod := terminalCleanupFixture()
			if test.change != nil {
				test.change(cluster, sts, pod)
			}
			deleted := 0
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster, sts).WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, obj client.ObjectList, _ ...client.ListOption) error {
					obj.(*corev1.PodList).Items = []corev1.Pod{*pod.DeepCopy()}
					return nil
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if target, ok := obj.(*corev1.Pod); ok {
						*target = *pod.DeepCopy()
						return nil
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleted++
					require.Equal(t, pod.UID, obj.GetUID())
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.EqualValues(t, 0, *options.GracePeriodSeconds)
					require.Equal(t, pod.UID, *options.Preconditions.UID)
					require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
					return nil
				},
			}).Build()
			requested, err := NewManager(c, testScheme, "").CompleteTerminatedPodDeletion(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Equal(t, test.allowed, requested)
			require.Equal(t, test.allowed, deleted == 1)
		})
	}
}

func terminalCleanupFixture() (*openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *corev1.Pod) {
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant", UID: "cluster-uid"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{Profile: openbaov1alpha1.ProfileDevelopment}}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, UID: "sts-uid",
		Labels:          map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator, constants.LabelOpenBaoCluster: cluster.Name},
		Annotations:     map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}}}
	now := metav1.Now()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sts.Name + "-0", Namespace: cluster.Namespace, UID: "pod-uid", ResourceVersion: "1",
		DeletionTimestamp: &now, Labels: map[string]string{constants.LabelOpenBaoCluster: cluster.Name,
			constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator, constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	return cluster, sts, pod
}

func activeCleanupContainer() []corev1.ContainerStatus {
	return []corev1.ContainerStatus{{Name: "active", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
}

func TestTerminalPodCleanupReadFailures(t *testing.T) {
	for _, stage := range []string{"list", "pod", "statefulset", "cluster"} {
		t.Run(stage, func(t *testing.T) {
			cluster, sts, pod := terminalCleanupFixture()
			unavailable := apierrors.NewServiceUnavailable("API unavailable")
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cluster, sts).WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, obj client.ObjectList, _ ...client.ListOption) error {
					if stage == "list" {
						return unavailable
					}
					obj.(*corev1.PodList).Items = []corev1.Pod{*pod.DeepCopy()}
					return nil
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					switch target := obj.(type) {
					case *corev1.Pod:
						if stage == "pod" {
							return unavailable
						}
						*target = *pod.DeepCopy()
						return nil
					case *appsv1.StatefulSet:
						if stage == "statefulset" {
							return unavailable
						}
					case *openbaov1alpha1.OpenBaoCluster:
						if stage == "cluster" {
							return unavailable
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
					t.Fatal("must not delete after an incomplete authority read")
					return nil
				},
			}).Build()
			requested, err := NewManager(c, testScheme, "").CompleteTerminatedPodDeletion(t.Context(), logr.Discard(), cluster)
			require.ErrorIs(t, err, unavailable)
			require.False(t, requested)
		})
	}
}
