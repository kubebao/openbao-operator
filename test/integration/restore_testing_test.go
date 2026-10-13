//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/deletionops"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/provisioner"
	"github.com/kubebao/openbao-operator/internal/service/restore"
)

func TestSourceDeletionDuringRejectedRestoreTestReleasesReservation(t *testing.T) {
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "rejected-restore-test")
	cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
	cluster.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 1 1 *", Image: "backup:dev", JWTAuthRole: "backup",
		Target: api.BackupTarget{Endpoint: "https://storage.example", Bucket: "snapshots"},
		RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: namespace,
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage-reader"},
			ClusterTemplate:      *newFreshRestoreRequest(namespace, "template").Spec.ClusterTemplate},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Initialized = true
		stamp := metav1.Now()
		status.Backup = &api.BackupStatus{SuccessfulBackups: 1, LastBackupName: "snapshot", LastBackupTime: &stamp,
			LatestSnapshot: &api.BackupSnapshotSummary{ClusterID: "source", Version: "2.7.0", Size: 123, Digest: "sha256:" + strings.Repeat("b", 64)}}
	})
	base := newControllerClient(t)
	creates := 0
	wrapped := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*api.OpenBaoRestore); !ok {
				return c.Create(ctx, obj, opts...)
			}
			if len((&client.CreateOptions{}).ApplyOptions(opts).DryRun) != 0 {
				return nil
			}
			creates++
			live := &api.OpenBaoCluster{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), live))
			require.NotNil(t, live.Status.Backup.RestoreTest.Active)
			require.NoError(t, c.Delete(ctx, live))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), live))
			require.Greater(t, live.Generation, cluster.Generation, "API-server deletion changes the source generation")
			return apierrors.NewForbidden(schema.GroupResource{Group: "openbao.org", Resource: "openbaorestores"}, obj.GetName(), fmt.Errorf("destination rejected creation"))
		},
	})
	manager := backup.NewManager(wrapped, k8sScheme, portopenbao.ClientConfig{}, nil, "").WithReader(wrapped).
		WithAdminOpsStatusMutator(adminopsstatus.NewMutator(wrapped, wrapped))
	_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 1, creates)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
	require.Equal(t, api.RestoreTestFailed, cluster.Status.Backup.RestoreTest.Last.Outcome)
	require.NoError(t, deletionops.Handle(ctx, logr.Discard(), deletionops.Dependencies{Client: k8sClient}, cluster))
	cluster.Finalizers = nil
	require.NoError(t, k8sClient.Update(ctx, cluster))
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)))
}

func TestRestoreTestReservesOneRequestAcrossRestarts(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	destination := newTestNamespace(t)
	setRestoreDestinationApproval(t, destination, "")
	controller := newImpersonatedClient(t, controllerUsername)
	for _, ns := range []string{namespace, destination} {
		require.NoError(t, newPrivilegedImpersonatedClient(t, provisionerUsername).Create(ctx, provisioner.GenerateTenantRole(ns)))
		require.NoError(t, newPrivilegedImpersonatedClient(t, provisionerUsername).Create(ctx, provisioner.GenerateTenantRoleBinding(ns,
			provisioner.OperatorServiceAccount{Namespace: "openbao-operator-system", Name: "openbao-operator-controller"})))
		waitForResourceAuthorization(t, controllerUsername, ns, "openbao.org", "openbaorestores", "", "create")
	}
	manager := func() *backup.Manager {
		return backup.NewManager(controller, k8sScheme, portopenbao.ClientConfig{}, nil, "").WithReader(controller).
			WithAdminOpsStatusMutator(adminopsstatus.NewMutator(controller, controller))
	}
	cluster := newMinimalClusterObj(namespace, "restore-test-source")
	cluster.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 1 1 *", Image: "backup:dev", JWTAuthRole: "backup",
		Target: api.BackupTarget{Endpoint: "https://storage.example", Bucket: "snapshots"},
		RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: destination,
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage-reader"},
			ClusterTemplate: api.RestoreClusterTemplate{Version: "2.7.0",
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-a"}, {Name: "registry-b"}}, Storage: api.StorageConfig{Size: "1Gi"},
				TLS:    api.TLSConfig{Enabled: true, Mode: api.TLSModeOperatorManaged, RotationPeriod: "720h"},
				Unseal: api.UnsealConfig{Type: "transit", Transit: &api.TransitSealConfig{Address: "https://seal.example", KeyName: "recovery", MountPath: "transit"}, CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit"}}},
		},
	}
	// Source write authority does not grant destination restore authority.
	username := "restore-test-source-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	candidate := cluster.DeepCopy()
	candidate.Spec.InitContainer = nil
	candidate.Spec.Backup.Image = ""
	err := newImpersonatedClient(t, username).Create(ctx, candidate, client.DryRunAll)
	require.ErrorContains(t, err, "create authority for destination openbaorestores")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	grantClusterOpenBaoVerbs(t, namespace, cluster.Name, username, "source-images", "usecustomexecutables")
	// The controller has no destination Secret-reader Role before a child exists.
	require.True(t, apierrors.IsForbidden(controller.Get(ctx, client.ObjectKey{Namespace: destination, Name: "transit"}, &corev1.Secret{})))
	before := cluster.DeepCopy()
	cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
	require.NoError(t, controller.Patch(ctx, cluster, client.MergeFrom(before)))
	before = cluster.DeepCopy()
	cluster.Annotations = map[string]string{"test": "metadata-only"}
	require.NoError(t, newImpersonatedClient(t, username).Patch(ctx, cluster, client.MergeFrom(before)))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Initialized = true
		stamp := metav1.Now()
		status.Backup = &api.BackupStatus{SuccessfulBackups: 1, LastBackupName: "snapshot", LastBackupTime: &stamp,
			LatestSnapshot: &api.BackupSnapshotSummary{ClusterID: "native-source", Version: "2.7.0", Size: 123, Digest: "sha256:" + strings.Repeat("a", 64)}}
	})
	_, err = manager().Reconcile(ctx, logr.Discard(), cluster)
	require.NoError(t, err)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
	require.Equal(t, "DestinationRejected", meta.FindStatusCondition(cluster.Status.Backup.RestoreTest.Conditions, "Passed").Reason)
	children := &api.OpenBaoRestoreList{}
	require.NoError(t, k8sClient.List(ctx, children, client.InNamespace(destination)))
	require.Empty(t, children.Items)

	setRestoreDestinationApproval(t, destination, "true")
	for i := 0; i < 4; i++ {
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		// A new Manager models process restart without an in-memory reservation.
		_, err := manager().Reconcile(ctx, logr.Discard(), cluster)
		require.NoError(t, err)
	}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	run := cluster.Status.Backup.RestoreTest.Active
	require.NotNil(t, run)
	require.NotEmpty(t, run.UID)
	require.NoError(t, k8sClient.List(ctx, children, client.InNamespace(destination)))
	require.Len(t, children.Items, 1)
	child := &children.Items[0]
	require.Equal(t, api.RestoreTargetLifecycleDisposable, child.Spec.TargetLifecycle)
	require.Equal(t, cluster.Status.Backup.LatestSnapshot.Digest, child.Spec.Source.ExpectedDigest)

	// Target admission must also succeed before the provisioner has granted
	// access to the referenced Secrets. The configuring user already delegated them.
	restoreManager := restore.NewManager(controller, k8sScheme, nil, nil, "").WithReader(controller)
	for range 3 {
		require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(child), child))
		_, err := restoreManager.Reconcile(ctx, logr.Discard(), child)
		require.NoError(t, err)
	}
	require.NotNil(t, child.Status.Target)
	require.NotEmpty(t, child.Status.Target.UID)
	target := &api.OpenBaoCluster{}
	require.NoError(t, controller.Get(ctx, client.ObjectKey{Namespace: destination, Name: child.Spec.Cluster}, target))
	require.Equal(t, child.Status.Target.UID, target.UID)
	require.True(t, apierrors.IsForbidden(controller.Get(ctx, client.ObjectKey{Namespace: destination, Name: "transit"}, &corev1.Secret{})))

	// Template StorageClass delegation is checked as the configuring user, even
	// though the controller creates the eventual cluster.
	grantNamespacedResourceVerbs(t, destination, username, "destination-delegation", "*", "*", nil, "*")
	// Blank StorageClass values follow the same default-class rule as cluster storage.
	for _, name := range []string{"", "  "} {
		candidate := cluster.DeepCopy()
		candidate.Spec.Backup.RestoreTest.ClusterTemplate.Storage.StorageClassName = ptr.To(name)
		require.NoError(t, newImpersonatedClient(t, username).Update(ctx, candidate, client.DryRunAll))
		manual := newFreshRestoreRequest(destination, "blank-storage")
		manual.Spec.ClusterTemplate.Storage.StorageClassName = ptr.To(name)
		require.NoError(t, newImpersonatedClient(t, username).Create(ctx, manual, client.DryRunAll))
	}

	candidate = cluster.DeepCopy()
	candidate.Spec.Backup.RestoreTest.ClusterTemplate.Storage.StorageClassName = ptr.To("restricted-test-storage")
	err = newImpersonatedClient(t, username).Patch(ctx, candidate, client.MergeFrom(cluster), client.DryRunAll)
	require.ErrorContains(t, err, "destination StorageClass")
	manual := child.DeepCopy()
	manual.ObjectMeta = metav1.ObjectMeta{Name: "manual-storage-check", Namespace: destination}
	manual.Status = api.OpenBaoRestoreStatus{}
	manual.Spec.ClusterTemplate.Storage.StorageClassName = ptr.To("restricted-test-storage")
	err = newImpersonatedClient(t, username).Create(ctx, manual, client.DryRunAll)
	require.ErrorContains(t, err, "their StorageClass")
	grantClusterScopedResourceVerbs(t, username, "restore-test-storage-"+namespace, "storage.k8s.io", "storageclasses", []string{"restricted-test-storage"}, "use")
	require.NoError(t, newImpersonatedClient(t, username).Create(ctx, manual, client.DryRunAll))
	require.NoError(t, newImpersonatedClient(t, username).Patch(ctx, candidate, client.MergeFrom(cluster), client.DryRunAll))

	setRestoreDestinationApproval(t, destination, "")
	// A finished child's result is saved before it is deleted. The next
	// reconciliation clears the reservation without starting the same count again.
	child.Status = api.OpenBaoRestoreStatus{Phase: api.RestorePhaseCompleted,
		Target: &api.RestoreTargetStatus{ReservedAt: metav1.Now(), AppliedAt: &metav1.Time{Time: time.Now()}, Cleanup: api.RestoreTargetCleanupComplete}}
	require.NoError(t, k8sClient.Status().Update(ctx, child))
	for i := 0; i < 4; i++ {
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		_, err := manager().Reconcile(ctx, logr.Discard(), cluster)
		require.NoError(t, err)
		if err := controller.Get(ctx, client.ObjectKeyFromObject(child), child); err == nil && child.DeletionTimestamp != nil {
			_, err = restoreManager.Reconcile(ctx, logr.Discard(), child)
			require.NoError(t, err)
		}
	}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
	require.NotNil(t, cluster.Status.Backup.RestoreTest.LastSuccessTime)
	require.Equal(t, api.RestoreTestPassed, cluster.Status.Backup.RestoreTest.Last.Outcome)
	require.NoError(t, k8sClient.List(ctx, children, client.InNamespace(destination)))
	require.Empty(t, children.Items)
	before = cluster.DeepCopy()
	cluster.Finalizers = nil
	require.NoError(t, controller.Patch(ctx, cluster, client.MergeFrom(before)))
}

func TestSourceDeletionWaitsForUnboundRestoreTest(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	cluster := createMinimalCluster(t, namespace, "deleting-source")
	cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
	require.NoError(t, k8sClient.Update(ctx, cluster))
	updateClusterStatus(t, cluster, func(status *api.OpenBaoClusterStatus) {
		status.Backup = &api.BackupStatus{RestoreTest: &api.RestoreTestStatus{
			Active: &api.RestoreTestRun{Name: "late-child", Namespace: namespace, StartedAt: metav1.Now()},
		}}
	})
	require.NoError(t, k8sClient.Delete(ctx, cluster))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	controller := newControllerClient(t)
	deps := deletionops.Dependencies{Client: controller}
	require.ErrorContains(t, deletionops.Handle(ctx, logr.Discard(), deps, cluster), "creation is uncertain")

	// The already submitted Create finishes after source deletion has started.
	child := newFreshRestoreRequest(namespace, "late-child")
	child.Annotations = map[string]string{constants.AnnotationRestoreTestSource: string(cluster.UID)}
	child.Finalizers = []string{api.OpenBaoRestoreFinalizer}
	require.NoError(t, controller.Create(ctx, child))
	require.ErrorContains(t, deletionops.Handle(ctx, logr.Discard(), deps, cluster), "waiting for disposable")
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Equal(t, child.UID, cluster.Status.Backup.RestoreTest.Active.UID)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(child), child))
	require.NotNil(t, child.DeletionTimestamp)
	child.Finalizers = nil
	require.NoError(t, controller.Update(ctx, child))
	require.NoError(t, deletionops.Handle(ctx, logr.Discard(), deps, cluster))
	cluster.Finalizers = nil
	require.NoError(t, controller.Update(ctx, cluster))
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)))
}
