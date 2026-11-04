//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

func TestRestoreTestReleaseAdmissionAndDeletion(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	controller := newControllerClient(t)
	for _, deleting := range []bool{false, true} {
		name := "release-active"
		if deleting {
			name = "release-deleting"
		}
		cluster := createMinimalCluster(t, namespace, name)
		cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
		require.NoError(t, k8sClient.Update(ctx, cluster))
		stamp := metav1.Now()
		mutate := adminopsstatus.NewMutator(controller, controller)
		require.NoError(t, mutate(ctx, cluster, func(current *api.OpenBaoCluster) error {
			current.Status.Backup = &api.BackupStatus{RestoreTest: &api.RestoreTestStatus{
				Active:          &api.RestoreTestRun{Name: "uncertain", Namespace: namespace, StartedAt: stamp},
				LastScheduledAt: &stamp, LastBackupCount: 7,
			}}
			return nil
		}, adminops.ForceOwnership))
		username := "release-" + name
		grantNamespacedResourceVerbs(t, namespace, username, name+"-edit", "kubebao.org", "openbaoclusters", []string{name}, "get", "patch", "usecustomexecutables")
		user := newImpersonatedClient(t, username)
		before := cluster.DeepCopy()
		cluster.Annotations = map[string]string{constants.AnnotationRestoreTestAcknowledge: "uncertain/Release"}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			err := user.Patch(ctx, cluster, client.MergeFrom(before), client.DryRunAll)
			require.ErrorContains(c, err, "restore permission")
		}, 10*time.Second, 100*time.Millisecond)
		grantNamespacedResourceVerbs(t, namespace, username, name+"-restore", "kubebao.org", "openbaoclusters", []string{name}, "restore")
		cluster.Annotations[constants.AnnotationRestoreTestAcknowledge] = "stale/Release"
		requireAdmissionDenied(t, user.Patch(ctx, cluster, client.MergeFrom(before)))
		cluster.Annotations[constants.AnnotationRestoreTestAcknowledge] = "uncertain/Release"
		if deleting {
			require.NoError(t, k8sClient.Delete(ctx, before))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(before), before))
			cluster = before.DeepCopy()
			cluster.Annotations = map[string]string{constants.AnnotationRestoreTestAcknowledge: "uncertain/Release"}
		}
		require.Eventually(t, func() bool {
			return user.Patch(ctx, cluster, client.MergeFrom(before)) == nil
		}, 10*time.Second, 100*time.Millisecond)
		if deleting {
			require.NoError(t, backup.CancelRestoreTest(ctx, controller, controller, mutate, cluster))
		} else {
			manager := backup.NewManager(controller, k8sScheme, portopenbao.ClientConfig{}, nil, "").WithReader(controller).WithAdminOpsStatusMutator(mutate)
			_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
			require.NoError(t, err)
		}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
		require.Equal(t, api.RestoreTestFailed, cluster.Status.Backup.RestoreTest.Last.Outcome)
		require.Equal(t, int64(7), cluster.Status.Backup.RestoreTest.LastBackupCount)
		// Removing a consumed annotation does not require restore permission again.
		before = cluster.DeepCopy()
		delete(cluster.Annotations, constants.AnnotationRestoreTestAcknowledge)
		require.NoError(t, user.Patch(ctx, cluster, client.MergeFrom(before)))
		if deleting {
			cluster.Finalizers = nil
			require.NoError(t, controller.Update(ctx, cluster))
		}
	}
}

func TestManagedRestartStatusRequiresController(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	request := newClaimRequest(t, namespace, "restart-status")
	before := request.DeepCopy()
	request.Status.Restart = &api.RestoreRestartStatus{Pods: []api.RestoreRestartPod{{Name: "target-0", UID: "pod", StatefulSetUID: "sts"}}}
	require.Eventually(t, func() bool {
		err := k8sClient.Status().Patch(ctx, request, client.MergeFrom(before), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "Only the controller can record managed")
	}, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, newControllerClient(t).Status().Patch(ctx, request, client.MergeFrom(before)))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.Equal(t, "target-0", request.Status.Restart.Pods[0].Name)
}

func TestRecordedRestoreDispositionRejectsConflictingAcknowledgement(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	controller := newControllerClient(t)
	for _, disposition := range []api.RestoreAdministratorDisposition{api.RestoreAdministratorResume, api.RestoreAdministratorAbandon} {
		request := newClaimRequest(t, namespace, "recorded-"+strings.ToLower(string(disposition)))
		before := request.DeepCopy()
		request.Status.Phase = api.RestorePhaseUnknown
		request.Status.AdministratorDisposition = disposition
		require.NoError(t, controller.Status().Patch(ctx, request, client.MergeFrom(before)))
		before = request.DeepCopy()
		request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/" + string(disposition)}
		require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
		before = request.DeepCopy()
		other := api.RestoreAdministratorResume
		if disposition == other {
			other = api.RestoreAdministratorAbandon
		}
		request.Annotations[constants.AnnotationRestoreAcknowledge] = string(request.UID) + "/" + string(other)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			err := k8sClient.Patch(ctx, request, client.MergeFrom(before), client.DryRunAll)
			require.ErrorContains(c, err, "Recovery has already been released")
		}, 10*time.Second, 100*time.Millisecond)
		delete(request.Annotations, constants.AnnotationRestoreAcknowledge)
		require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
	}

	request := newClaimRequest(t, namespace, "unfinished-restart")
	before := request.DeepCopy()
	request.Status.Phase = api.RestorePhaseUnknown
	request.Status.Restart = &api.RestoreRestartStatus{Pods: []api.RestoreRestartPod{{Name: "target-0", UID: "pod", StatefulSetUID: "sts"}}}
	require.NoError(t, controller.Status().Patch(ctx, request, client.MergeFrom(before)))
	before = request.DeepCopy()
	request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Abandon"}
	require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
}
