//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/restore"
)

func TestRestoreTargetRecoverySurvivesManagerReplacement(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, tc := range []struct {
		name    string
		edit    func(*api.OpenBaoRestore)
		phase   api.RestorePhase
		cleanup api.RestoreTargetCleanup
	}{
		{name: "released-hold", cleanup: api.RestoreTargetCleanupComplete, phase: api.RestorePhaseFailed},
		{name: "interrupted-restart", edit: func(r *api.OpenBaoRestore) {
			r.Status.Restart = &api.RestoreRestartStatus{Pods: []api.RestoreRestartPod{
				{Name: r.Spec.Cluster + "-0", UID: "original-pod", StatefulSetUID: "original-sts"},
			}}
		}, cleanup: api.RestoreTargetCleanupComplete, phase: api.RestorePhaseFailed},
		{name: "completed-receipt", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = api.RestoreTargetCleanupComplete
			r.Status.Target.AppliedAt = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		}, cleanup: api.RestoreTargetCleanupComplete, phase: api.RestorePhaseCompleted},
		{name: "unapplied-receipt", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = api.RestoreTargetCleanupComplete
		}, cleanup: api.RestoreTargetCleanupComplete, phase: api.RestorePhaseFailed},
		{name: "cleanup-delay", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = ""
			r.Status.Target.AppliedAt = &metav1.Time{Time: time.Now()}
			r.Spec.CleanupAfterSeconds = 3600
		}, phase: api.RestorePhaseUnknown},
		{name: "expired-delay", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = ""
			r.Status.Target.AppliedAt = &metav1.Time{Time: time.Now().Add(-time.Hour)}
			r.Spec.CleanupAfterSeconds = 1
		}, cleanup: api.RestoreTargetCleanupComplete, phase: api.RestorePhaseCompleted},
		{name: "retained-handoff", edit: func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleRetain
			r.Status.Target.Cleanup = ""
			r.Status.Target.AppliedAt = &metav1.Time{Time: time.Now()}
		}, phase: api.RestorePhaseUnknown},
		{name: "abandoned-cleanup", edit: func(r *api.OpenBaoRestore) {
			r.Status.AdministratorDisposition = api.RestoreAdministratorAbandon
		}, cleanup: api.RestoreTargetCleanupPending, phase: api.RestorePhaseUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namespace := newTestNamespace(t)
			setRestoreDestinationApproval(t, namespace, "true")
			controller := newControllerClient(t)
			request := newFreshRestoreRequest(namespace, tc.name)
			request.Finalizers = []string{api.OpenBaoRestoreFinalizer}
			request.Spec.TargetLifecycle = api.RestoreTargetLifecycleDisposable
			request.Status.Phase = api.RestorePhaseUnknown
			request.Status.Target = &api.RestoreTargetStatus{
				ReservedAt: metav1.Now(), BootstrapClusterID: "bootstrap", Cleanup: api.RestoreTargetCleanupPending,
			}
			if tc.edit != nil {
				tc.edit(request)
			}
			status := request.Status.DeepCopy()
			require.NoError(t, k8sClient.Create(ctx, request))
			cluster := newMinimalClusterObj(namespace, request.Spec.Cluster)
			cluster.Annotations = map[string]string{constants.AnnotationRestoreOrigin: string(request.UID)}
			// Started cleanup has already released the hold; recovery must not
			// return to application observation or restart acknowledgement.
			require.NoError(t, controller.Create(ctx, cluster))
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: "data-" + cluster.Name + "-0",
					Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
			require.NoError(t, controller.Create(ctx, pvc))
			status.Target.UID = cluster.UID
			status.Target.DataPVCUID = pvc.UID
			if status.Target.Cleanup == api.RestoreTargetCleanupComplete {
				// A persisted completion receipt follows deletion of both objects.
				require.NoError(t, controller.Delete(ctx, cluster))
				require.NoError(t, k8sClient.Delete(ctx, pvc))
			}
			request.Status = *status
			require.NoError(t, controller.Status().Update(ctx, request))
			persistedTarget := request.Status.Target.DeepCopy()

			for range 8 {
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
				manager := restore.NewManager(controller, k8sScheme, nil, nil, "").WithReader(controller)
				_, err := manager.Reconcile(ctx, logr.Discard(), request)
				require.NoError(t, err)
				// Envtest has no garbage collector. Complete only its foreground
				// deletion handshake after the manager issues a real API deletion.
				err = k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
				if err == nil && cluster.DeletionTimestamp != nil {
					require.Empty(t, cluster.Annotations[constants.AnnotationRestoreHold])
					cluster.Finalizers = nil
					require.NoError(t, newRestoreModelGarbageCollectorClient(t).Update(ctx, cluster))
				} else {
					require.True(t, err == nil || apierrors.IsNotFound(err), "%v", err)
				}
				// The API adds PVC protection without starting the controller
				// that releases it. No Pod uses this fixture's volume.
				err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)
				if err == nil && pvc.DeletionTimestamp != nil {
					pvc.Finalizers = nil
					require.NoError(t, k8sClient.Update(ctx, pvc))
				} else {
					require.True(t, err == nil || apierrors.IsNotFound(err), "%v", err)
				}
			}
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
			require.Equal(t, tc.phase, request.Status.Phase)
			require.Equal(t, tc.cleanup, request.Status.Target.Cleanup)
			require.Equal(t, persistedTarget.ReservedAt, request.Status.Target.ReservedAt)
			require.Equal(t, persistedTarget.AppliedAt, request.Status.Target.AppliedAt)
			require.Equal(t, persistedTarget.UID, request.Status.Target.UID)
			require.Equal(t, persistedTarget.DataPVCUID, request.Status.Target.DataPVCUID)
			if tc.cleanup != api.RestoreTargetCleanupComplete {
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
				require.Nil(t, cluster.DeletionTimestamp)
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc))
			} else {
				require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)))
				require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)))
			}
			jobs := &batchv1.JobList{}
			require.NoError(t, k8sClient.List(ctx, jobs, client.InNamespace(namespace)))
			require.Empty(t, jobs.Items, "recovery must not submit another restore")
		})
	}
}
