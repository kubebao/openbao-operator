//go:build integration

package integration

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/rolling"
)

func TestRollingRetryWaitsForForegroundExecutorRetirement(t *testing.T) {
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "retire-executor")
	cluster.Spec.Upgrade = &api.UpgradeConfig{
		JWTAuthRole: "upgrade", Requests: &api.UpgradeRequestConfig{Retry: "retry-now"},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Initialized, status.CurrentVersion = true, testPreviousOpenBaoVersion
	})
	controller := newControllerClient(t)
	mutate := adminopsstatus.NewMutator(controller, controller)
	require.NoError(t, mutate(ctx, cluster, func(obj *api.OpenBaoCluster) error {
		obj.Status.Upgrade = &api.UpgradeProgress{
			ExecutionID: "old-execution", CurrentPartition: 3,
			FromVersion: obj.Status.CurrentVersion, TargetVersion: obj.Spec.Version,
			Failure: &api.ControllerErrorStatus{Reason: upgrade.ReasonStepDownTimeout},
		}
		return nil
	}, adminops.RespectOwnership))
	runID := upgrade.RollingStepDownRunID(cluster, cluster.Name+"-2")
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  namespace,
			Name:       upgrade.ExecutorJobName(cluster.Name, upgrade.ExecutorActionRollingStepDownLeader, runID, "", ""),
			Finalizers: []string{"test.openbao.org/hold"},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "executor", Image: "executor:test"}},
		}}},
	}
	require.NoError(t, opslifecycle.PrepareManagedJobOwner(job, cluster, k8sScheme))
	require.NoError(t, controller.Create(ctx, job))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "old-executor", Namespace: namespace, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
			Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
		}}},
		Spec: job.Spec.Template.Spec,
	}
	require.NoError(t, controller.Create(ctx, pod))
	newManager := func() *rolling.Manager {
		return rolling.NewManager(controller, k8sScheme, nil, portopenbao.ClientConfig{}, nil, "").
			WithAdminOpsStatusMutator(mutate)
	}
	_, err := newManager().Reconcile(ctx, logr.Discard(), cluster)
	require.ErrorIs(t, err, operatorerrors.ErrTransientKubernetesAPI)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job))
	require.NotNil(t, job.DeletionTimestamp)
	require.Contains(t, job.Finalizers, metav1.FinalizerDeleteDependents)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, "old-execution", cluster.Status.Upgrade.ExecutionID)
	require.NotNil(t, cluster.Status.Upgrade.Failure)
	require.True(t, upgrade.RetryRequestPending(cluster))

	// EnvTest has no garbage collector. Complete foreground deletion only after
	// removing the dependent executor Pod, then resume with a new manager.
	require.NoError(t, controller.Delete(ctx, pod, client.GracePeriodSeconds(0)))
	job.Finalizers = nil
	require.NoError(t, controller.Update(ctx, job))
	_, err = newManager().Reconcile(ctx, logr.Discard(), cluster)
	require.ErrorIs(t, err, operatorerrors.ErrTransientClusterState,
		"retry succeeds before the missing-workload health gate")
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.NotEmpty(t, cluster.Status.Upgrade.ExecutionID)
	require.NotEqual(t, "old-execution", cluster.Status.Upgrade.ExecutionID)
	require.Nil(t, cluster.Status.Upgrade.Failure)
	require.False(t, upgrade.RetryRequestPending(cluster))
	accepted := cluster.Status.Upgrade.ExecutionID
	_, err = newManager().Reconcile(ctx, logr.Discard(), cluster)
	require.ErrorIs(t, err, operatorerrors.ErrTransientClusterState)
	require.Equal(t, accepted, cluster.Status.Upgrade.ExecutionID)
}
