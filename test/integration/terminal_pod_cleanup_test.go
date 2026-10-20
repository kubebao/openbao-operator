//go:build integration

package integration

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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	clusterapp "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestTerminalPodCleanupRealAPI(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	operator := newControllerClient(t)
	syncer := newPrivilegedImpersonatedClient(t, "kubernetes-super-admin")
	for _, scenario := range []string{
		"failed", "succeeded", "running", "pending", "not-deleting", "finalizer",
		"pod-version-race", "replacement-race", "statefulset-replacement", "cluster-replacement", "pause-race",
	} {
		t.Run(scenario, func(t *testing.T) {
			cluster, sts, pod := createTerminalCleanupFixture(t, scenario)
			err := syncer.Delete(ctx, pod.DeepCopy(), client.GracePeriodSeconds(0), client.DryRunAll)
			require.ErrorContains(t, err, "denied request: Direct modification")
			writer := interceptor.NewClient(operator, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.Equal(t, pod.UID, *options.Preconditions.UID)
					require.NotEmpty(t, *options.Preconditions.ResourceVersion)
					if scenario == "pod-version-race" {
						current := &corev1.Pod{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), current))
						current.Annotations = map[string]string{"test.openbao.org/race": "changed"}
						require.NoError(t, c.Update(ctx, current))
					}
					if scenario == "replacement-race" {
						require.NoError(t, c.Delete(ctx, pod.DeepCopy(), client.GracePeriodSeconds(0), client.Preconditions{UID: &pod.UID}))
						newPod := pod.DeepCopy()
						newPod.ResourceVersion, newPod.UID = "", ""
						newPod.DeletionTimestamp, newPod.DeletionGracePeriodSeconds = nil, nil
						newPod.Status = corev1.PodStatus{}
						require.NoError(t, c.Create(ctx, newPod))
						require.NotEqual(t, pod.UID, newPod.UID)
					}
					return c.Delete(ctx, obj, opts...)
				},
			})
			reader := interceptor.NewClient(newControllerClient(t), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, readingCluster := obj.(*openbaov1alpha1.OpenBaoCluster); readingCluster && scenario == "pause-race" {
						current := cluster.DeepCopy()
						current.Spec.Paused = true
						require.NoError(t, k8sClient.Update(ctx, current))
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			if scenario == "statefulset-replacement" {
				require.NoError(t, operator.Delete(ctx, sts))
				sts.UID, sts.ResourceVersion = "", ""
				require.NoError(t, operator.Create(ctx, sts))
			}
			if scenario == "cluster-replacement" {
				require.NoError(t, k8sClient.Delete(ctx, cluster))
				replacement := cluster.DeepCopy()
				replacement.UID, replacement.ResourceVersion = "", ""
				require.NoError(t, k8sClient.Create(ctx, replacement))
			}
			requested, err := workload.NewManager(writer, k8sScheme, "").WithReader(reader).
				CompleteTerminatedPodDeletion(ctx, logr.Discard(), cluster)
			if scenario == "pod-version-race" || scenario == "replacement-race" {
				require.True(t, apierrors.IsConflict(err), "expected API-server precondition conflict, got %v", err)
			} else {
				require.NoError(t, err)
			}
			allowed := scenario == "failed" || scenario == "succeeded"
			require.Equal(t, allowed, requested)
			stored := &corev1.Pod{}
			getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), stored)
			if allowed {
				require.True(t, apierrors.IsNotFound(getErr), "old Pod must be gone: %v", getErr)
			} else {
				require.NoError(t, getErr)
				if scenario == "replacement-race" {
					require.NotEqual(t, pod.UID, stored.UID)
					require.Nil(t, stored.DeletionTimestamp)
				} else {
					require.Equal(t, pod.UID, stored.UID)
					require.Equal(t, pod.Finalizers, stored.Finalizers)
				}
			}
		})
	}
}

func TestTerminalPodCleanupApplication(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	cluster, _, pod := createTerminalCleanupFixture(t, "failed")
	r := clusterapp.NewTerminalPodCleanupReconciler(newControllerClient(t), k8sClient)
	result, err := r.Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})))
}

type terminalCleanupContinuation struct{ called bool }

func (r *terminalCleanupContinuation) Reconcile(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	r.called = true
	return recon.Result{}, nil
}

func TestTerminalPodCleanupConflictDoesNotBlockWorkload(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	cluster, _, pod := createTerminalCleanupFixture(t, "failed")
	operator := newControllerClient(t)
	conflicted := false
	writer := interceptor.NewClient(operator, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			current := &corev1.Pod{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), current))
			current.Annotations = map[string]string{"test.openbao.org/race": "changed"}
			require.NoError(t, c.Update(ctx, current))
			err := c.Delete(ctx, obj, opts...)
			conflicted = apierrors.IsConflict(err)
			return err
		},
	})
	next := &terminalCleanupContinuation{}
	app := clusterapp.NewApplications(clusterapp.ApplicationsConfig{
		Client: operator, WorkloadPolicy: clusterapp.DefaultWorkloadResultPolicy(),
		WorkloadReconcilers: []clusterapp.SubReconciler{
			clusterapp.NewTerminalPodCleanupReconciler(writer, operator), next,
		},
	})
	_, err := app.ReconcileWorkload(ctx, logr.Discard(), cluster.DeepCopy(), cluster, nil)
	require.NoError(t, err)
	require.True(t, conflicted, "the delete must produce an API-server precondition conflict")
	require.True(t, next.called, "cleanup failure must not block later workload steps")
	stored := &corev1.Pod{}
	require.NoError(t, operator.Get(ctx, client.ObjectKeyFromObject(pod), stored))
	require.Equal(t, pod.UID, stored.UID)
	require.Nil(t, cluster.Status.Workload.LastError)
}

func createTerminalCleanupFixture(t *testing.T, scenario string) (*openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, *corev1.Pod) {
	t.Helper()
	namespace := newTestNamespace(t)
	cluster := createMinimalCluster(t, namespace, "cleanup")
	operator := newControllerClient(t)
	labels := map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster: cluster.Name, constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: namespace, Labels: labels,
		Annotations:     map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}},
		Spec: appsv1.StatefulSetSpec{ServiceName: cluster.Name, Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "openbao", Image: "example.com/app:test"}}}}}}
	require.NoError(t, operator.Create(ctx, sts))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sts.Name + "-0", Namespace: namespace, Labels: labels,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
		Spec: corev1.PodSpec{NodeName: "test-node", Containers: []corev1.Container{{Name: "openbao", Image: "example.com/app:test"}}}}
	if scenario == "finalizer" {
		pod.Finalizers = []string{"test.openbao.org/hold"}
	}
	creator := newPrivilegedImpersonatedClient(t, "system:serviceaccount:kube-system:statefulset-controller")
	require.NoError(t, creator.Create(ctx, pod))
	if scenario != "not-deleting" {
		require.NoError(t, creator.Delete(ctx, pod.DeepCopy(), client.GracePeriodSeconds(600)))
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod))
		require.NotNil(t, pod.DeletionTimestamp)
	}
	pod.Status.Phase = corev1.PodFailed
	if scenario == "succeeded" {
		pod.Status.Phase = corev1.PodSucceeded
	}
	if scenario == "running" {
		pod.Status.Phase = corev1.PodRunning
	}
	if scenario == "pending" {
		pod.Status.Phase = corev1.PodPending
	}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))
	return cluster, sts, pod
}
