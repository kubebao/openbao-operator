package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/adapter/security"
	"github.com/dc-tec/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	"github.com/dc-tec/openbao-operator/internal/platform/testutil/robustness"
	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
)

// testLogger returns a no-op logger for testing.
func testLogger() logr.Logger {
	return logr.Discard()
}

func withTestAdminOpsStatusPersistence(manager *Manager, k8sClient client.Client) *Manager {
	return manager.WithAdminOpsStatusMutator(adminopsstatus.NewMutator(k8sClient, k8sClient))
}

const statusSubresourceName = "status"

type operationLockConflictProbe struct {
	injected      bool
	patchAttempts int
}

func operationLockConflictOnce(
	t *testing.T,
	cluster *openbaov1alpha1.OpenBaoCluster,
	currentOwner *openbaov1alpha1.OperationLockStatus,
	probe *operationLockConflictProbe,
) interceptor.Funcs {
	t.Helper()
	return interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if subResourceName == statusSubresourceName {
				if _, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok {
					probe.patchAttempts++
					if !probe.injected {
						latest := &openbaov1alpha1.OpenBaoCluster{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), latest))
						latest.Status.OperationLock = currentOwner.DeepCopy()
						require.NoError(t, c.Status().Update(ctx, latest))
						probe.injected = true
						return apierrors.NewConflict(
							schema.GroupResource{Group: openbaov1alpha1.GroupVersion.Group, Resource: "openbaoclusters"},
							cluster.Name,
							errors.New("operation lock owner changed"),
						)
					}
				}
			}
			return c.Status().Patch(ctx, obj, patch, opts...)
		},
	}
}

// setTestResourceVersion sets ResourceVersion on test objects for fake client SSA compatibility.
// Controller-runtime 0.23 sets ResourceVersion for SSA operations, so test objects need it to avoid conflicts.
func setTestResourceVersion(obj metav1.Object) {
	if obj.GetResourceVersion() == "" {
		obj.SetResourceVersion("1")
	}
	if obj.GetUID() == "" {
		obj.SetUID(types.UID(obj.GetName() + "-uid"))
	}
}

func managedRestoreJobForRestore(
	job *batchv1.Job,
	restore *openbaov1alpha1.OpenBaoRestore,
) *batchv1.Job {
	if job.UID == "" {
		job.UID = types.UID(job.Name + "-uid")
	}
	controller := true
	job.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoRestore",
		Name:       restore.Name,
		UID:        restore.UID,
		Controller: &controller,
	}}
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	job.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(restore.UID)
	return job
}

func managedVoterStatefulSetForCluster(
	statefulSet *appsv1.StatefulSet,
	cluster *openbaov1alpha1.OpenBaoCluster,
) *appsv1.StatefulSet {
	controller := true
	statefulSet.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoCluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
		Controller: &controller,
	}}
	if statefulSet.Annotations == nil {
		statefulSet.Annotations = map[string]string{}
	}
	statefulSet.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(cluster.UID)
	return statefulSet
}

// TestRestoreJobName tests the deterministic job name generation.
func TestRestoreJobName(t *testing.T) {
	tests := []struct {
		name        string
		restoreName string
		wantPrefix  string
	}{
		{
			name:        "simple name",
			restoreName: "my-restore",
			wantPrefix:  RestoreJobNamePrefix + "my-restore",
		},
		{
			name:        "with namespace-like name",
			restoreName: "ns-restore-backup",
			wantPrefix:  RestoreJobNamePrefix + "ns-restore-backup",
		},
		{
			name:        "short name",
			restoreName: "r",
			wantPrefix:  RestoreJobNamePrefix + "r",
		},
		{
			name:        "long name is truncated with hash suffix",
			restoreName: "restore-request-e2e-claims-functional-restore-1776963121-462553",
			wantPrefix:  RestoreJobNamePrefix + "restore-request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.restoreName,
				},
			}

			got := restoreJobName(restore)
			assert.True(t, strings.HasPrefix(got, tt.wantPrefix), "restoreJobName() should preserve the expected prefix")
			assert.LessOrEqual(t, len(got), 63, "restoreJobName() must fit Kubernetes label limits")
			assert.Equal(t, got, restoreJobName(restore), "restoreJobName() should be deterministic")
		})
	}
}

// TestRestoreServiceAccountName tests the service account naming.
func TestRestoreServiceAccountName(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		wantSuffix  string
	}{
		{
			name:        "simple cluster",
			clusterName: "my-cluster",
			wantSuffix:  "my-cluster" + RestoreServiceAccountSuffix,
		},
		{
			name:        "long cluster name",
			clusterName: "production-openbao-cluster",
			wantSuffix:  "production-openbao-cluster" + RestoreServiceAccountSuffix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.clusterName,
				},
			}

			got := restoreServiceAccountName(cluster)
			assert.Equal(t, tt.wantSuffix, got, "restoreServiceAccountName() should use cluster name + suffix")
		})
	}
}

// TestRestoreLabels tests the standard labels generation.
func TestRestoreLabels(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	got := restoreLabels(cluster)

	assert.NotNil(t, got)
	assert.Equal(t, "test-cluster", got["openbao.org/cluster"])
	assert.Equal(t, ComponentRestore, got["openbao.org/component"])
	assert.Contains(t, got, "app.kubernetes.io/managed-by")
}

// TestReconcilePending tests the Pending to Validating phase transition.
func TestReconcilePending(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-restore",
			Namespace:       "default",
			ResourceVersion: "1", // Set initial ResourceVersion for fake client SSA compatibility
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhasePending,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handlePending(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0, "should requeue after pending")

	// Verify status was updated
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseValidating, updated.Status.Phase)
	assert.NotNil(t, updated.Status.StartTime)
	assert.Equal(t, "backup-key", updated.Status.SnapshotKey)
}

func TestReconcilePending_AfterGet(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-restore",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhasePending,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	fetched := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, fetched))

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handlePending(context.Background(), testLogger(), fetched)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0, "should requeue after pending")
}

// TestReconcilePhaseRouting tests that Reconcile correctly routes by phase.
func TestReconcilePhaseRouting(t *testing.T) {
	tests := []struct {
		name           string
		phase          openbaov1alpha1.RestorePhase
		expectRequeue  bool
		expectNoAction bool
	}{
		{
			name:          "pending phase transitions to validating",
			phase:         openbaov1alpha1.RestorePhasePending,
			expectRequeue: true,
		},
		{
			name:           "completed phase is terminal",
			phase:          openbaov1alpha1.RestorePhaseCompleted,
			expectNoAction: true,
		},
		{
			name:           "failed phase is terminal",
			phase:          openbaov1alpha1.RestorePhaseFailed,
			expectNoAction: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-restore",
					Namespace:  "default",
					Finalizers: []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
				},
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Cluster: "test-cluster",
				},
				Status: openbaov1alpha1.OpenBaoRestoreStatus{
					Phase: tt.phase,
				},
			}
			setTestResourceVersion(restore)

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(restore).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
				WithReturnManagedFields().
				Build()

			mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

			result, err := mgr.Reconcile(context.Background(), testLogger(), restore)
			require.NoError(t, err)

			if tt.expectNoAction {
				assert.Equal(t, int64(0), int64(result.RequeueAfter), "terminal phases should not requeue")
			}
			if tt.expectRequeue {
				assert.True(t, result.RequeueAfter > 0, "should requeue")
			}
		})
	}
}

func TestValidateClusterState_WaitsForSteadyReadReplicaDrain(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-cluster",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{Replicas: 2},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
		},
	}

	replicas := int32(1)
	readSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster-read",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			ReadyReplicas:      1,
			CurrentReplicas:    1,
		},
	}

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-restore",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, readSTS, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")
	result, err := mgr.validateClusterState(context.Background(), testLogger(), restore, cluster)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, restoreRequeueImmediately, result.RequeueAfter)

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: restore.Name, Namespace: restore.Namespace}, updated))
	assert.Contains(t, updated.Status.Message, "Waiting for steady read replicas to scale down before restore starts")
}

func TestReconcileTerminalPhase_ReleasesOperationLock(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "cluster-uid",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseCompleted,
		},
	}
	setTestResourceVersion(restore)

	var patchPayloads []string
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == statusSubresourceName {
					payload, err := patch.Data(obj)
					if err != nil {
						return err
					}
					patchPayloads = append(patchPayloads, string(payload))
				}
				return c.Status().Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.Reconcile(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, int64(0), int64(result.RequeueAfter))
	require.Len(t, patchPayloads, 1, "expected one optimistic lock clear patch")
	assert.Contains(t, patchPayloads[0], `"operationLock":null`)
	assert.Contains(t, patchPayloads[0], `"resourceVersion":`)
}

func TestReconcileTerminalPhase_RetriesLockReleaseAfterTransientFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "cluster-uid",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseCompleted,
		},
	}
	setTestResourceVersion(restore)

	failStatusPatchOnce := true
	var patchPayloads []string
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == statusSubresourceName {
					payload, err := patch.Data(obj)
					if err != nil {
						return err
					}
					patchPayloads = append(patchPayloads, string(payload))
					if failStatusPatchOnce {
						failStatusPatchOnce = false
						return apierrors.NewInternalError(fmt.Errorf("transient status patch failure"))
					}
				}
				return c.Status().Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	// First terminal reconcile fails lock release due to transient patch error.
	result, err := mgr.Reconcile(context.Background(), testLogger(), restore)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to release cluster operation lock for terminal restore")
	assert.Equal(t, int64(0), int64(result.RequeueAfter))
	assert.False(t, failStatusPatchOnce, "expected injected transient patch failure to be consumed")
	require.Len(t, patchPayloads, 1, "first terminal reconcile should fail on the first lock patch")

	// Second terminal reconcile should succeed and clear the lock.
	freshRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, freshRestore))

	result, err = mgr.Reconcile(context.Background(), testLogger(), freshRestore)
	require.NoError(t, err)
	assert.Equal(t, int64(0), int64(result.RequeueAfter))
	require.Len(t, patchPayloads, 2, "second terminal reconcile should retry the lock clear patch")
	assert.Contains(t, patchPayloads[1], `"operationLock":null`)
	assert.Contains(t, patchPayloads[1], `"resourceVersion":`)
}

func TestHandleValidating_ForceRetryUsesCurrentNonRestoreOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationBackup,
				Holder:    "openbao-adminops-support-controller/backup",
				Message:   "backup job",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
			UID:       "restore-uid",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:               "test-cluster",
			JWTAuthRole:           "restore-role",
			OverrideOperationLock: true,
			Force:                 true,
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	currentOwner := &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationUpgrade,
		Holder:    "openbao-adminops-controller/upgrade",
		Message:   "upgrade in progress",
	}
	conflictProbe := &operationLockConflictProbe{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		WithInterceptorFuncs(operationLockConflictOnce(t, cluster, currentOwner, conflictProbe)).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)
	assert.True(t, conflictProbe.injected, "expected the first acquire patch to conflict")
	assert.Equal(t, 2, conflictProbe.patchAttempts, "expected the acquire to retry after the conflict")

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, updatedRestore.Status.Phase)

	foundOverrideCondition := false
	for i := range updatedRestore.Status.Conditions {
		cond := updatedRestore.Status.Conditions[i]
		if cond.Type != constants.ConditionTypeOperationLockOverride {
			continue
		}
		foundOverrideCondition = true
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, constants.ReasonOperationLockOverridden, cond.Reason)
		assert.Contains(t, cond.Message, "operation=Upgrade")
		assert.Contains(t, cond.Message, "holder=openbao-adminops-controller/upgrade")
		assert.NotContains(t, cond.Message, "openbao-adminops-support-controller/backup")
		break
	}
	assert.True(t, foundOverrideCondition, "expected operation lock override condition to be persisted")
}

func TestHandleValidating_ForceRetryDoesNotReplaceCurrentRestoreOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationBackup,
				Holder:    "openbao-adminops-support-controller/backup",
				Message:   "backup job",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:               "test-cluster",
			OverrideOperationLock: true,
			Force:                 true,
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	currentOwner := &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationRestore,
		Holder:    constants.ControllerNameOpenBaoRestore + "/other-restore",
		Message:   "restore default/other-restore",
	}
	conflictProbe := &operationLockConflictProbe{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		WithInterceptorFuncs(operationLockConflictOnce(t, cluster, currentOwner, conflictProbe)).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)
	assert.True(t, conflictProbe.injected, "expected the first acquire patch to conflict")
	assert.Equal(t, 1, conflictProbe.patchAttempts, "expected the retry to reject the current restore owner before patching")

	updatedCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updatedCluster))
	require.NotNil(t, updatedCluster.Status.OperationLock)
	assert.Equal(t, currentOwner.Operation, updatedCluster.Status.OperationLock.Operation)
	assert.Equal(t, currentOwner.Holder, updatedCluster.Status.OperationLock.Holder)

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseValidating, updatedRestore.Status.Phase)
	assert.Contains(t, updatedRestore.Status.Message, "operation=Restore")
	assert.Contains(t, updatedRestore.Status.Message, currentOwner.Holder)
	assert.Nil(t, meta.FindStatusCondition(updatedRestore.Status.Conditions, constants.ConditionTypeOperationLockOverride))
}

func TestHandleValidating_SetsActionableOperationLockWaitMessage(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationBackup,
				Holder:    "controller/backup",
				Message:   "backup job backup-test-cluster",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:     "test-cluster",
			JWTAuthRole: "restore-role",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Contains(t, updated.Status.Message, "operation=Backup")
	assert.Contains(t, updated.Status.Message, "holder=controller/backup")
	assert.Contains(t, updated.Status.Message, "Restore will retry automatically")
}

func TestHandleRunning_RestoreJobAlreadyExistsDuringCreate(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Replicas: 3,
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:     "test-cluster",
			Image:       "ghcr.io/example/restore:0.1.0",
			JWTAuthRole: "restore",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://s3.example.com",
					Bucket:   "example-backups",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	setTestResourceVersion(restore)
	restore.Status.Execution = newRestoreExecutionStatus(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*batchv1.Job); ok {
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, obj.GetName())
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, result.RequeueAfter, "phase=%s stage=%s message=%s", restore.Status.Phase, restore.Status.Execution.Stage, restore.Status.Message)
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updated))
	require.NotNil(t, updated.Status.Execution)
	assert.Equal(t, openbaov1alpha1.RestoreExecutionStageCreated, updated.Status.Execution.Stage)
	assert.Equal(t, restoreJobName(restore), updated.Status.Execution.JobName)
	assert.NotNil(t, updated.Status.Execution.CommittedAt)
	assert.NotNil(t, updated.Status.Execution.CreatedAt)
}

func TestHandleRunning_DoesNotRecreateMissingCommittedJob(t *testing.T) {
	tests := []struct {
		name  string
		stage openbaov1alpha1.RestoreExecutionStage
	}{
		{name: "creation committed", stage: openbaov1alpha1.RestoreExecutionStageCommitted},
		{name: "creation receipt persisted", stage: openbaov1alpha1.RestoreExecutionStageCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			require.NoError(t, batchv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
			}
			setTestResourceVersion(cluster)
			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "test-restore", Namespace: "default"},
				Spec:       openbaov1alpha1.OpenBaoRestoreSpec{Cluster: cluster.Name},
				Status:     openbaov1alpha1.OpenBaoRestoreStatus{Phase: openbaov1alpha1.RestorePhaseRunning},
			}
			setTestResourceVersion(restore)
			restore.Status.Execution = newRestoreExecutionStatus(restore)
			restore.Status.Execution.Stage = tt.stage
			if tt.stage == openbaov1alpha1.RestoreExecutionStageCreated {
				restore.Status.Execution.JobUID = types.UID("missing-job-uid")
			}

			jobCreates := 0
			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cluster, restore).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*batchv1.Job); ok {
							jobCreates++
						}
						return c.Create(ctx, obj, opts...)
					},
				}).
				Build()

			mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")
			result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			assert.Zero(t, jobCreates)

			updated := &openbaov1alpha1.OpenBaoRestore{}
			require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updated))
			assert.Equal(t, openbaov1alpha1.RestorePhaseUnknown, updated.Status.Phase)
			require.NotNil(t, updated.Status.Execution)
			assert.Equal(t, openbaov1alpha1.RestoreExecutionStageUnknown, updated.Status.Execution.Stage)
			assert.Contains(t, updated.Status.Message, "will not recreate")
		})
	}
}

func TestHandleRunning_RejectsForeignSucceededJob(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-cluster",
		Namespace: "default",
	}}
	setTestResourceVersion(cluster)
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: cluster.Name},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	setTestResourceVersion(restore)
	foreignJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreJobName(restore),
			Namespace: restore.Namespace,
		},
		Status: batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, foreignJob).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		Build()
	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.ErrorContains(t, err, "requires managed controller owner OpenBaoRestore")
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, restore.Status.Phase)
	assert.Nil(t, restore.Status.CompletionTime)
}

func TestHandleRunning_FailedJobSetsActionableMessage(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	setTestResourceVersion(restore)

	job := managedRestoreJobForRestore(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreJobName(restore),
			Namespace: "default",
		},
		Status: batchv1.JobStatus{
			Failed: 1,
			Conditions: []batchv1.JobCondition{
				{
					Type:    batchv1.JobFailed,
					Status:  corev1.ConditionTrue,
					Message: "pod exited with status 1",
				},
			},
		},
	}, restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, int64(0), int64(result.RequeueAfter))

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "pod exited with status 1")
	assert.Contains(t, updated.Status.Message, "kubectl logs job/")
	assert.Contains(t, updated.Status.Message, "create a new OpenBaoRestore to retry")
}

func TestHandleRunning_SucceededJobWaitsForSteadyReadReplicaRestore(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	restartCompletedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{
				Replicas: 2,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Restore: &openbaov1alpha1.ClusterRestoreStatus{
				Name:               "test-restore",
				UID:                "restore-uid",
				RestartCompletedAt: &restartCompletedAt,
			},
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
			ReadReplicas: &openbaov1alpha1.ReadReplicaStatus{
				DesiredReplicas:    2,
				ReadyReplicas:      1,
				RegisteredReplicas: 1,
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
			UID:       "restore-uid",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	setTestResourceVersion(restore)

	job := managedRestoreJobForRestore(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreJobName(restore),
			Namespace: "default",
		},
		Status: batchv1.JobStatus{
			Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}, restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, restoreRequeueImmediately, result.RequeueAfter)

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: restore.Name, Namespace: restore.Namespace}, updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, updatedRestore.Status.Phase)
	assert.Nil(t, updatedRestore.Status.CompletionTime)
	assert.Contains(t, updatedRestore.Status.Message, "Waiting for steady read replicas to restore before marking restore complete")

	updatedCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updatedCluster))
	require.NotNil(t, updatedCluster.Status.OperationLock)
	assert.Equal(t, openbaov1alpha1.ClusterOperationRestore, updatedCluster.Status.OperationLock.Operation)
}

func TestHandleRunning_SucceededJobCompletesAfterSteadyReadReplicaRestore(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	restartCompletedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{
				Replicas: 2,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Restore: &openbaov1alpha1.ClusterRestoreStatus{
				Name:               "test-restore",
				UID:                "restore-uid",
				RestartCompletedAt: &restartCompletedAt,
			},
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
			ReadReplicas: &openbaov1alpha1.ReadReplicaStatus{
				DesiredReplicas:    2,
				ReadyReplicas:      2,
				RegisteredReplicas: 2,
			},
			Conditions: []metav1.Condition{
				{Type: string(openbaov1alpha1.ConditionReadReplicasReady), Status: metav1.ConditionTrue},
				{Type: string(openbaov1alpha1.ConditionReadServingAvailable), Status: metav1.ConditionTrue},
				{Type: string(openbaov1alpha1.ConditionRaftMembershipReady), Status: metav1.ConditionTrue},
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
			UID:       "restore-uid",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
	setTestResourceVersion(restore)

	job := managedRestoreJobForRestore(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreJobName(restore),
			Namespace: "default",
		},
		Status: batchv1.JobStatus{
			Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}, restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: restore.Name, Namespace: restore.Namespace}, updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseCompleted, updatedRestore.Status.Phase)
	assert.NotNil(t, updatedRestore.Status.CompletionTime)

}

func TestHandleRunning_SucceededJobRequestsVoterRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	replicas := int32(3)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: replicas},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "test-restore", Namespace: "default", UID: "restore-uid"},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source:  openbaov1alpha1.RestoreSource{Key: "snapshot-key"},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{Phase: openbaov1alpha1.RestorePhaseRunning},
	}
	setTestResourceVersion(restore)

	job := managedRestoreJobForRestore(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: restoreJobName(restore), Namespace: "default"},
		Status:     batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}, restore)
	statefulSet := managedVoterStatefulSetForCluster(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Generation: 2},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			Replicas:           replicas,
			ReadyReplicas:      replicas,
			UpdatedReplicas:    replicas,
			CurrentReplicas:    replicas,
			CurrentRevision:    "old-revision",
			UpdateRevision:     "old-revision",
		},
	}, cluster)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job, statefulSet).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()
	mgr := withTestAdminOpsStatusPersistence(
		NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), ""),
		k8sClient,
	)

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, restoreRequeueImmediately, result.RequeueAfter)

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, updatedRestore.Status.Phase)
	assert.Contains(t, updatedRestore.Status.Message, "Waiting for voter Pods to restart")

	updatedCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updatedCluster))
	require.NotNil(t, updatedCluster.Status.Restore)
	assert.Equal(t, restore.Name, updatedCluster.Status.Restore.Name)
	assert.Equal(t, string(restore.UID), updatedCluster.Status.Restore.UID)
	assert.Nil(t, updatedCluster.Status.Restore.RestartCompletedAt)
	require.NotNil(t, updatedCluster.Status.OperationLock)
}

func TestPostRestoreVoterRestartComplete_RejectsUnownedStatefulSet(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3},
	}
	setTestResourceVersion(cluster)
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "test-restore", Namespace: "default", UID: "restore-uid"},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, statefulSet).
		Build()
	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	complete, message, err := mgr.postRestoreVoterRestartComplete(context.Background(), cluster, restore)
	require.ErrorContains(t, err, "requires OpenBaoCluster owner proof")
	assert.False(t, complete)
	assert.Empty(t, message)
}

func TestHandleRunning_SucceededJobCompletesAfterVoterRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	replicas := int32(3)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: replicas},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			Restore:     &openbaov1alpha1.ClusterRestoreStatus{Name: "test-restore", UID: "restore-uid"},
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationRestore,
				Holder:    constants.ControllerNameOpenBaoRestore + "/test-restore",
				Message:   "restore default/test-restore",
			},
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "test-restore", Namespace: "default", UID: "restore-uid"},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source:  openbaov1alpha1.RestoreSource{Key: "snapshot-key"},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{Phase: openbaov1alpha1.RestorePhaseRunning},
	}
	setTestResourceVersion(restore)

	job := managedRestoreJobForRestore(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: restoreJobName(restore), Namespace: "default"},
		Status:     batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}, restore)
	statefulSet := managedVoterStatefulSetForCluster(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name, Namespace: cluster.Namespace, Generation: 2},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				constants.AnnotationRestoreRevision: string(restore.UID),
			}}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			Replicas:           replicas,
			ReadyReplicas:      replicas,
			UpdatedReplicas:    replicas,
			CurrentReplicas:    replicas,
			CurrentRevision:    "restored-revision",
			UpdateRevision:     "restored-revision",
		},
	}, cluster)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job, statefulSet).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()
	mgr := withTestAdminOpsStatusPersistence(
		NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), ""),
		k8sClient,
	)

	result, err := mgr.handleRunning(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	updatedRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), updatedRestore))
	assert.Equal(t, openbaov1alpha1.RestorePhaseCompleted, updatedRestore.Status.Phase)
	assert.Contains(t, updatedRestore.Status.Message, "post-restore recovery")
	require.NotNil(t, updatedRestore.Status.Execution)
	assert.Equal(t, openbaov1alpha1.RestoreExecutionStageFollowThroughComplete, updatedRestore.Status.Execution.Stage)
	assert.Equal(t, openbaov1alpha1.RestoreExecutionResultSucceeded, updatedRestore.Status.Execution.TerminalResult)
	assert.NotNil(t, updatedRestore.Status.Execution.TerminalObservedAt)
	assert.NotNil(t, updatedRestore.Status.Execution.FollowThroughCompletedAt)

	updatedCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), updatedCluster))
	require.NotNil(t, updatedCluster.Status.Restore)
	assert.NotNil(t, updatedCluster.Status.Restore.RestartCompletedAt)
	assert.Nil(t, updatedCluster.Status.OperationLock)
}

func TestReconcilePending_AddsFinalizerThenPatchesStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-restore",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhasePending,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.Reconcile(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)
}

// TestValidatingClusterNotFound tests validation failure when cluster doesn't exist.
func TestValidatingClusterNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "nonexistent-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	// No cluster object in the fake client
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err) // failRestore returns nil error

	// Verify status was updated to Failed
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "not found")
}

// TestValidatingUninitializedCluster tests validation with uninitialized cluster.
func TestValidatingUninitializedCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: false, // Not initialized
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
			Force: false, // Force is not set
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)

	// Verify status was updated to Failed
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "not initialized")
}

// TestValidatingNoAuthentication tests validation failure when no auth is configured.
func TestValidatingNoAuthentication(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true, // Cluster is initialized
		},
	}
	setTestResourceVersion(cluster)

	// Restore with NO auth configured - neither jwtAuthRole nor tokenSecretRef
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
			},
			// JWTAuthRole and TokenSecretRef are NOT set
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err) // failRestore returns nil error

	// Verify status was updated to Failed with auth error message
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "authentication is required")
	configuration := meta.FindStatusCondition(updated.Status.Conditions, RestoreConfigurationConditionType)
	if configuration == nil {
		t.Fatalf("expected %s condition", RestoreConfigurationConditionType)
	}
	assert.Equal(t, metav1.ConditionFalse, configuration.Status)
	assert.Equal(t, constants.ReasonAuthenticationRequired, configuration.Reason)
}

func TestHandleValidating_RejectsUnlabeledStaticRestoreTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:        "test-cluster",
			TokenSecretRef: &corev1.LocalObjectReference{Name: "restore-token"},
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Bucket:   "backups",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-token",
			Namespace: "default",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, tokenSecret).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, constants.LabelOpenBaoCluster)
	configuration := meta.FindStatusCondition(updated.Status.Conditions, RestoreConfigurationConditionType)
	if configuration == nil {
		t.Fatalf("expected %s condition", RestoreConfigurationConditionType)
	}
	assert.Equal(t, metav1.ConditionFalse, configuration.Status)
	assert.Equal(t, constants.ReasonTokenSecretInvalid, configuration.Reason)
}

func TestHandleValidating_AcceptsLabeledStaticRestoreTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:        "test-cluster",
			TokenSecretRef: &corev1.LocalObjectReference{Name: "restore-token"},
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Bucket:   "backups",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-token",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelOpenBaoCluster:           "test-cluster",
				constants.LabelOpenBaoCredentialPurpose: constants.LabelValueCredentialPurposeRestoreToken,
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, tokenSecret).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, updated.Status.Phase)
	configuration := meta.FindStatusCondition(updated.Status.Conditions, RestoreConfigurationConditionType)
	if configuration == nil {
		t.Fatalf("expected %s condition", RestoreConfigurationConditionType)
	}
	assert.Equal(t, metav1.ConditionTrue, configuration.Status)
	assert.Contains(t, configuration.Message, "token Secret default/restore-token")
}

func TestHandleValidating_SetsRestoreConfigurationConditionForAmbientIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
		},
	}
	setTestResourceVersion(cluster)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster:     "test-cluster",
			JWTAuthRole: "restore-role",
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-key",
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Bucket:   "backups",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseValidating,
		},
	}
	setTestResourceVersion(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoRestore{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleValidating(context.Background(), testLogger(), restore)
	require.NoError(t, err)

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Equal(t, openbaov1alpha1.RestorePhaseRunning, updated.Status.Phase)
	configuration := meta.FindStatusCondition(updated.Status.Conditions, RestoreConfigurationConditionType)
	if configuration == nil {
		t.Fatalf("expected %s condition", RestoreConfigurationConditionType)
	}
	assert.Equal(t, metav1.ConditionTrue, configuration.Status)
	assert.Equal(t, constants.ReasonAmbientIdentityAssumed, configuration.Reason)
	assert.Contains(t, configuration.Message, "generated ServiceAccount")
}

func TestRestoreJobFailedStatusMessage_AppendsFailureHint(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-test",
			Namespace: "default",
		},
		Status: batchv1.JobStatus{
			Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
		},
	}

	message := restoreJobFailedStatusMessage(job, "Verify the generated ServiceAccount identity binding.")
	assert.Contains(t, message, "kubectl logs job/restore-test -n default")
	assert.Contains(t, message, "generated ServiceAccount identity binding")
}

// TestGetRestoreExecutorImage tests restore image resolution.
func TestGetRestoreExecutorImage(t *testing.T) {
	tests := []struct {
		name          string
		restoreImage  string
		clusterImage  string
		operatorRepo  string
		operatorTag   string
		expectedImage string
		expectError   bool
	}{
		{
			name:          "restore image takes precedence",
			restoreImage:  "custom/restore:v1",
			clusterImage:  "custom/backup:v2",
			expectedImage: "custom/restore:v1",
			expectError:   false,
		},
		{
			name:          "fallback to cluster backup image",
			restoreImage:  "",
			clusterImage:  "custom/backup:v2",
			expectedImage: "custom/backup:v2",
			expectError:   false,
		},
		{
			name:          "fallback to operator default backup image",
			restoreImage:  "",
			clusterImage:  "",
			operatorRepo:  "custom/backup",
			operatorTag:   "v3",
			expectedImage: "custom/backup:v3",
			expectError:   false,
		},
		{
			name:          "no image specified and no operator default returns error",
			restoreImage:  "",
			clusterImage:  "",
			expectedImage: "",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(constants.EnvOperatorBackupImageRepo, tt.operatorRepo)
			t.Setenv(constants.EnvOperatorVersion, tt.operatorTag)

			restore := &openbaov1alpha1.OpenBaoRestore{
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Image: tt.restoreImage,
				},
			}

			var backupSchedule *openbaov1alpha1.BackupSchedule
			if tt.clusterImage != "" {
				backupSchedule = &openbaov1alpha1.BackupSchedule{
					Image: tt.clusterImage,
				}
			}
			cluster := &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Backup: backupSchedule,
				},
			}

			got, err := getRestoreExecutorImage(restore, cluster)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedImage, got)
			}
		})
	}
}

// TestBuildRestoreEnvVars tests environment variable generation.
func TestBuildRestoreEnvVars(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Source: openbaov1alpha1.RestoreSource{
				Key: "backup-2024-01-01.snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint:     "https://s3.example.com",
					Bucket:       "my-bucket",
					Region:       "us-east-1",
					UsePathStyle: true,
				},
			},
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	envVars := buildRestoreEnvVars(restore, cluster)

	// Verify required env vars are present
	envMap := make(map[string]string)
	for _, env := range envVars {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	assert.Equal(t, "restore", envMap["EXECUTOR_MODE"])
	assert.Equal(t, "test-cluster", envMap["CLUSTER_NAME"])
	assert.Equal(t, "default", envMap["CLUSTER_NAMESPACE"])
	assert.Equal(t, "3", envMap["CLUSTER_REPLICAS"])
	assert.Equal(t, "backup-2024-01-01.snap", envMap["RESTORE_KEY"])
	assert.Equal(t, "my-bucket", envMap["RESTORE_BUCKET"])
	assert.Equal(t, "https://s3.example.com", envMap["RESTORE_ENDPOINT"])
	assert.Equal(t, "false", envMap[constants.EnvRestoreForce])
	assert.Equal(t, "restore-role", envMap["BACKUP_JWT_AUTH_ROLE"])
	assert.Equal(t, "jwt", envMap["BACKUP_AUTH_METHOD"])
}

// TestBuildRestoreVolumes tests volume generation.
func TestBuildRestoreVolumes(t *testing.T) {
	tests := []struct {
		name                string
		tlsEnabled          bool
		jwtAuthRole         string
		tokenSecretRef      *corev1.LocalObjectReference
		credentialsSecret   *corev1.LocalObjectReference
		expectedVolumeNames []string
	}{
		{
			name:                "TLS only",
			tlsEnabled:          true,
			expectedVolumeNames: []string{"tls-ca"},
		},
		{
			name:                "JWT auth",
			tlsEnabled:          true,
			jwtAuthRole:         "restore-role",
			expectedVolumeNames: []string{"tls-ca", "jwt-token"},
		},
		{
			name:                "token auth",
			tlsEnabled:          true,
			tokenSecretRef:      &corev1.LocalObjectReference{Name: "token-secret"},
			expectedVolumeNames: []string{"tls-ca", "restore-token"},
		},
		{
			name:                "with storage credentials",
			tlsEnabled:          true,
			credentialsSecret:   &corev1.LocalObjectReference{Name: "s3-creds"},
			expectedVolumeNames: []string{"tls-ca", "storage-credentials"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := &openbaov1alpha1.OpenBaoRestore{
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					JWTAuthRole:    tt.jwtAuthRole,
					TokenSecretRef: tt.tokenSecretRef,
					Source: openbaov1alpha1.RestoreSource{
						Target: openbaov1alpha1.BackupTarget{
							CredentialsSecretRef: tt.credentialsSecret,
						},
					},
				},
			}

			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					TLS: openbaov1alpha1.TLSConfig{Enabled: tt.tlsEnabled},
				},
			}

			tlsTrust, err := portopenbao.ResolveClientTrustBundle(cluster)
			require.NoError(t, err)

			volumes := buildRestoreVolumes(restore, cluster, tlsTrust)

			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}

			for _, expected := range tt.expectedVolumeNames {
				assert.Contains(t, volumeNames, expected, "should contain volume %s", expected)
			}
			if tt.tokenSecretRef != nil {
				for i := range volumes {
					if volumes[i].Name != restoreTokenVolumeName {
						continue
					}
					require.NotNil(t, volumes[i].Secret)
					require.NotNil(t, volumes[i].Secret.DefaultMode)
					assert.Equal(t, int32(0400), *volumes[i].Secret.DefaultMode)
				}
			}
		})
	}
}

// TestBuildRestoreVolumeMounts tests volume mount generation.
func TestBuildRestoreVolumeMounts(t *testing.T) {
	restore := &openbaov1alpha1.OpenBaoRestore{
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			JWTAuthRole: "restore-role",
		},
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			TLS: openbaov1alpha1.TLSConfig{Enabled: true},
		},
	}

	tlsTrust, err := portopenbao.ResolveClientTrustBundle(cluster)
	require.NoError(t, err)

	mounts := buildRestoreVolumeMounts(restore, cluster, tlsTrust)

	assert.Len(t, mounts, 2) // TLS + JWT
	for _, mount := range mounts {
		assert.True(t, mount.ReadOnly, "all mounts should be read-only for security")
	}
}

// TestFinalizer tests finalizer addition and removal.
func TestEnsureFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	err := mgr.ensureFinalizer(context.Background(), restore)
	require.NoError(t, err)

	// Verify finalizer was added
	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Contains(t, updated.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)

	// Second call should be idempotent
	restore.Finalizers = updated.Finalizers
	err = mgr.ensureFinalizer(context.Background(), restore)
	require.NoError(t, err)
}

func TestEnsureFinalizer_TransientPatchFailureThenSuccess(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
	}

	expected := errors.New("transient patch failure")
	injector := robustness.NewInjector(map[robustness.Operation]robustness.Rule{
		robustness.OpPatch: robustness.Once(expected),
	})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		WithInterceptorFuncs(injector.InterceptorFuncs()).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	firstErr := mgr.ensureFinalizer(context.Background(), restore)
	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "failed to add finalizer")
	assert.Contains(t, firstErr.Error(), expected.Error())

	freshRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, freshRestore))
	require.NotContains(t, freshRestore.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)

	require.NoError(t, mgr.ensureFinalizer(context.Background(), freshRestore))

	updated := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-restore", Namespace: "default"}, updated))
	assert.Contains(t, updated.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)

	require.NoError(t, mgr.ensureFinalizer(context.Background(), updated))
}

// TestHandleDeletion tests the deletion handling.
func TestHandleDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-restore",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "nonexistent-cluster", // Cluster doesn't exist
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleDeletion(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, int64(0), int64(result.RequeueAfter))

	// The handleDeletion removed the finalizer. With a deletion timestamp set,
	// the fake client now removes the object entirely, which is correct behavior.
	// We don't need to verify finalizers since successful return indicates
	// the finalizer was removed and deletion proceeded.
}

func TestHandleDeletion_KeepsFinalizerWhenLockReleaseFails(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-restore",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "test-cluster"},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore).
		Build()
	expected := errors.New("transient cluster read failure")
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*openbaov1alpha1.OpenBaoCluster); ok {
					return expected
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "").WithReader(reader)
	_, err := mgr.handleDeletion(context.Background(), testLogger(), restore)
	require.Error(t, err)
	assert.ErrorContains(t, err, expected.Error())

	current := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), current))
	assert.Contains(t, current.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
}

func TestHandleDeletion_DrainsCommittedRestoreJob(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "running-restore",
			Namespace:         "default",
			UID:               types.UID("running-restore-uid"),
			DeletionTimestamp: &now,
			Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "test-cluster"},
	}
	setTestResourceVersion(restore)
	restore.Status.Phase = openbaov1alpha1.RestorePhaseRunning
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	restore.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCreated
	lock := restoreOperationLock(restore)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: restore.Spec.Cluster, Namespace: restore.Namespace},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: lock.Operation,
				Holder:    lock.Holder,
			},
		},
	}
	setTestResourceVersion(cluster)
	job := managedRestoreJobForRestore(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      restoreJobName(restore),
			Namespace: restore.Namespace,
			UID:       types.UID("running-restore-job-uid"),
		}},
		restore,
	)
	job.Annotations[restoreExecutionIDAnnotation] = restore.Status.Execution.OperationID
	restore.Status.Execution.JobUID = job.UID

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore, cluster, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}).
		Build()
	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	result, err := mgr.handleDeletion(context.Background(), testLogger(), restore)
	require.NoError(t, err)
	assert.Equal(t, restoreRequeueJobPoll, result.RequeueAfter)

	currentCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), currentCluster))
	require.NotNil(t, currentCluster.Status.OperationLock)
	assert.Equal(t, lock.Holder, currentCluster.Status.OperationLock.Holder)
	currentRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), currentRestore))
	assert.Contains(t, currentRestore.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
	assert.NoError(t, k8sClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: restore.Namespace, Name: restoreJobName(restore)},
		&batchv1.Job{},
	))
}

func TestHandleDeletion_RejectsForeignRestoreJob(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "running-restore",
			Namespace:         "default",
			UID:               types.UID("running-restore-uid"),
			DeletionTimestamp: &now,
			Finalizers:        []string{openbaov1alpha1.OpenBaoRestoreFinalizer},
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "test-cluster"},
	}
	setTestResourceVersion(restore)
	lock := restoreOperationLock(restore)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: restore.Spec.Cluster, Namespace: restore.Namespace},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: lock.Operation,
				Holder:    lock.Holder,
			},
		},
	}
	setTestResourceVersion(cluster)
	foreignController := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      restoreJobName(restore),
		Namespace: restore.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "foreign-controller",
				UID:        types.UID("foreign-controller-uid"),
				Controller: &foreignController,
			},
		},
	}}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(restore, cluster, job).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		Build()
	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	_, err := mgr.handleDeletion(context.Background(), testLogger(), restore)
	require.Error(t, err)
	assert.ErrorContains(t, err, "requires managed controller owner OpenBaoRestore")

	currentJob := &batchv1.Job{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(job), currentJob))
	currentCluster := &openbaov1alpha1.OpenBaoCluster{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cluster), currentCluster))
	require.NotNil(t, currentCluster.Status.OperationLock)
	assert.Equal(t, lock.Holder, currentCluster.Status.OperationLock.Holder)
	currentRestore := &openbaov1alpha1.OpenBaoRestore{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(restore), currentRestore))
	assert.Contains(t, currentRestore.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
}

// TestReleaseClusterLock tests the cluster lock release.
func TestReleaseClusterLock_ClusterNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "nonexistent-cluster",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	err := mgr.releaseClusterLock(context.Background(), testLogger(), restore)
	require.NoError(t, err, "should not error when cluster not found")
}

// TestReleaseClusterLock_EmptyCluster tests release with empty cluster name.
func TestReleaseClusterLock_EmptyCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "", // Empty cluster name
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	err := mgr.releaseClusterLock(context.Background(), testLogger(), restore)
	require.NoError(t, err, "should return nil for empty cluster name")
}

// TestNewManagerCreation tests the manager constructor.
func TestNewManager(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := NewManager(k8sClient, scheme, nil, security.NewImageVerifier(testLogger(), k8sClient, nil), "")

	assert.NotNil(t, mgr)
	assert.NotNil(t, mgr.client)
	assert.NotNil(t, mgr.scheme)
}
