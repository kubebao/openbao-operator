package primitives

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// restoreRecord is an in-place restore an administrator resumed: the common
// finished record, with controller-only recovery evidence.
func restoreRecord(t *testing.T, mutate func(*api.OpenBaoRestore)) (*unstructured.Unstructured, ImportRequest) {
	t.Helper()
	now := metav1.Now()
	restore := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-1", Namespace: "tenant", UID: "restore-source-uid",
			ResourceVersion: "9", Generation: 1, Finalizers: []string{api.OpenBaoRestoreFinalizer}},
		Spec: api.OpenBaoRestoreSpec{Cluster: "bao", JWTAuthRole: "restore",
			Source: api.RestoreSource{Key: "clusters/bao/snapshot.snap"}},
		Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseUnknown,
			AdministratorDisposition: api.RestoreAdministratorResume,
			Execution: &api.RestoreExecutionStatus{TargetUID: "source-uid", OperationID: "restore-source-uid",
				Stage: api.RestoreExecutionStageTerminalObserved, JobName: "bao-restore-1",
				TerminalResult: api.RestoreExecutionResultSucceeded},
			Restart: &api.RestoreRestartStatus{Pods: []api.RestoreRestartPod{{Name: "bao-0", UID: "pod-uid",
				StatefulSetUID: "sts-uid"}}, CompletedAt: &now},
			Conditions: []metav1.Condition{{Type: constants.RestoreRecoveryReleasedConditionType,
				Status: metav1.ConditionTrue, Reason: "Resumed", ObservedGeneration: 1, LastTransitionTime: now}}},
	}
	if mutate != nil {
		mutate(restore)
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(restore)
	require.NoError(t, err)
	source := &unstructured.Unstructured{Object: object}
	source.SetAPIVersion(PlanSourceAPIVersion)
	source.SetKind(discoveryRestoreKind)
	return source, ImportRequest{Source: identity(PlanSourceAPIVersion, discoveryRestoreKind, source),
		NamespaceUID: "namespace-uid", MigrationID: "migration-test"}
}

func TestRestoreRecordsMustBeFinished(t *testing.T) {
	for name, mutate := range map[string]func(*api.OpenBaoRestore){
		"resumed":   nil,
		"completed": func(r *api.OpenBaoRestore) { r.Status.Phase = api.RestorePhaseCompleted },
		"failed before submission": func(r *api.OpenBaoRestore) {
			r.Status.Phase, r.Status.AdministratorDisposition, r.Status.Restart = api.RestorePhaseFailed, "", nil
		},
		"abandoned during restart": func(r *api.OpenBaoRestore) {
			r.Status.AdministratorDisposition, r.Status.Restart.CompletedAt = api.RestoreAdministratorAbandon, nil
		},
		"cleaned-up disposable target": func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleDisposable
			r.Status.Phase, r.Status.AdministratorDisposition = api.RestorePhaseCompleted, ""
			r.Status.Target = &api.RestoreTargetStatus{Cleanup: api.RestoreTargetCleanupComplete}
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, _ := restoreRecord(t, mutate)
			require.NoError(t, validateRestoreSource(source))
		})
	}
	for name, test := range map[string]struct {
		mutate  func(*api.OpenBaoRestore)
		message string
	}{
		"running": {func(r *api.OpenBaoRestore) { r.Status.Phase = api.RestorePhaseRunning }, "still running"},
		"pending": {func(r *api.OpenBaoRestore) { r.Status.Phase = "" }, "still running"},
		"awaiting acknowledgement": {func(r *api.OpenBaoRestore) {
			r.Status.AdministratorDisposition = ""
		}, "acknowledge Resume or Abandon"},
		"restarting": {func(r *api.OpenBaoRestore) { r.Status.Restart.CompletedAt = nil }, "restart is still in progress"},
		"cleanup failed": {func(r *api.OpenBaoRestore) {
			r.Status.Phase, r.Status.AdministratorDisposition = api.RestorePhaseFailed, ""
			r.Status.Target = &api.RestoreTargetStatus{Cleanup: api.RestoreTargetCleanupFailed}
		}, "cleanup failed"},
		"disposable target alive": {func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleDisposable
			r.Status.Phase, r.Status.AdministratorDisposition = api.RestorePhaseCompleted, ""
			r.Status.Target = &api.RestoreTargetStatus{Cleanup: api.RestoreTargetCleanupPending}
		}, "not cleaned up"},
		"owned": {func(r *api.OpenBaoRestore) {
			r.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "x"}}
		}, "owner references"},
	} {
		t.Run(name, func(t *testing.T) {
			source, _ := restoreRecord(t, test.mutate)
			require.ErrorContains(t, validateRestoreSource(source), test.message)
		})
	}
}

func TestImportKeepsRestoreStatusUnchanged(t *testing.T) {
	source, request := restoreRecord(t, nil)
	reader := sourceClient(t, source, nil, interceptor.Funcs{})
	payload, err := BuildImport(t.Context(), reader, request)
	require.NoError(t, err)
	status, _, _ := unstructured.NestedMap(source.Object, "status")
	require.Equal(t, status, payload.Status, "restore status is history and keeps its conditions")
	require.Equal(t, PlanDestinationAPIVersion, payload.Object.GetAPIVersion())
	require.Equal(t, discoveryRestoreKind, payload.Object.GetKind())
	require.Empty(t, payload.Object.GetFinalizers())
	require.Equal(t, "restore-source-uid", payload.Object.GetAnnotations()[GateSourceUIDAnnotation])

	inspection, err := InspectSource(t.Context(), reader, request)
	require.NoError(t, err)
	require.Equal(t, discoveryRestoreKind, inspection.Source.Kind)
}

func restoreRoots() (TransferIdentity, TransferIdentity) {
	return TransferIdentity{APIVersion: PlanSourceAPIVersion, Kind: discoveryRestoreKind, Namespace: "tenant",
			Name: "restore-1", UID: "restore-source-uid"},
		TransferIdentity{APIVersion: PlanDestinationAPIVersion, Kind: discoveryRestoreKind, Namespace: "tenant",
			Name: "restore-1", UID: "restore-destination-uid"}
}

// restoreJob matches the released restore controller's Job: labelled with its
// Cluster, with owner-uid provenance and a controller reference naming the record.
func restoreJob(owner TransferIdentity) (metav1.PartialObjectMetadata, metav1.PartialObjectMetadata) {
	job := discoveryTestMetadata(testBatchAPIVersion, discoveryJobKind, "bao-restore-1")
	job.Labels = map[string]string{constants.LabelOpenBaoCluster: "bao",
		constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator}
	job.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(owner.UID),
		"openbao.org/restore-execution-id": string(owner.UID)}
	job.OwnerReferences = []metav1.OwnerReference{{APIVersion: owner.APIVersion, Kind: owner.Kind, Name: owner.Name,
		UID: owner.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}}
	pod := discoveryTestMetadata("v1", "Pod", "bao-restore-1-x7k2")
	pod.Labels = job.Labels
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: testBatchAPIVersion, Kind: discoveryJobKind,
		Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}
	return job, pod
}

func TestDiscoveryMovesRestoreJobsWithTheirRecord(t *testing.T) {
	source, destination := restoreRoots()
	job, pod := restoreJob(source)
	// A Cluster resource whose name matches the record is never the record's.
	unrelated := discoveryTestManaged("v1", "ConfigMap", "restore-1-config")
	unrelated.Labels = map[string]string{constants.LabelOpenBaoCluster: "restore-1"}
	options := DiscoveryOptions{NamespaceUID: "namespace-uid", Source: source, Destination: destination}
	result, err := DiscoverManagedResources(t.Context(), &discoveryTestReader{
		objects: []metav1.PartialObjectMetadata{job, pod, unrelated}}, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 1)
	require.Equal(t, discoveryIdentity(&job), result.Transfers[0].Resource)
	require.Equal(t, []DiscoveryDescendant{{Resource: discoveryIdentity(&pod), Owner: discoveryIdentity(&job)}},
		result.Descendants)

	forged := job.DeepCopy()
	forged.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestOtherUID
	result, err = DiscoverManagedResources(t.Context(), &discoveryTestReader{
		objects: []metav1.PartialObjectMetadata{*forged}}, options)
	require.NoError(t, err)
	require.Len(t, result.Blockers, 1, "provenance naming another owner contradicts the controller reference")

	_, err = DiscoverManagedResources(t.Context(), &discoveryTestReader{}, DiscoveryOptions{
		NamespaceUID: "namespace-uid", Source: source, Destination: destination,
		RetainedResources: []TransferIdentity{discoveryIdentity(&unrelated)}})
	require.ErrorContains(t, err, "no retained resources")
}

func TestClusterDiscoveryLeavesRestoreJobsToTheirRecord(t *testing.T) {
	source, destination := restoreRoots()
	job, pod := restoreJob(source)
	reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{job, pod}}

	result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
	require.NoError(t, err)
	require.NotEmpty(t, result.Blockers, "without the record nominated, its Job has no provable custody")

	options := discoveryTestOptions()
	options.ForeignRoots = []TransferIdentity{source, destination}
	result, err = DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Empty(t, result.Transfers)
	require.ElementsMatch(t, []DiscoveryDescendant{
		{Resource: discoveryIdentity(&job), Owner: source},
		{Resource: discoveryIdentity(&pod), Owner: discoveryIdentity(&job)},
	}, result.Descendants)

	options.ForeignRoots = []TransferIdentity{{APIVersion: PlanSourceAPIVersion, Kind: discoveryClusterKind,
		Namespace: "tenant", Name: "other", UID: "other"}}
	_, err = DiscoverManagedResources(t.Context(), reader, options)
	require.ErrorContains(t, err, "foreign discovery roots")
}

func TestTransferMovesARestoreJobWithItsRecord(t *testing.T) {
	source, destination := restoreRoots()
	metadata, _ := restoreJob(source)
	job := &batchv1.Job{ObjectMeta: *metadata.ObjectMeta.DeepCopy()}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant", UID: "namespace-uid"}}
	objects := make([]client.Object, 0, 4)
	objects = append(objects, namespace, job)
	for _, root := range []TransferIdentity{source, destination} {
		owner := &unstructured.Unstructured{}
		owner.SetAPIVersion(root.APIVersion)
		owner.SetKind(root.Kind)
		owner.SetNamespace(root.Namespace)
		owner.SetName(root.Name)
		owner.SetUID(root.UID)
		objects = append(objects, owner)
	}
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	request := TransferRequest{NamespaceUID: "namespace-uid", Source: source, Destination: destination,
		Resource: TransferIdentity{APIVersion: testBatchAPIVersion, Kind: discoveryJobKind, Namespace: "tenant",
			Name: job.Name, UID: job.UID}}

	require.NoError(t, TransferOwnership(t.Context(), c, request))
	moved := &batchv1.Job{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(job), moved))
	require.Equal(t, []metav1.OwnerReference{{APIVersion: destination.APIVersion, Kind: destination.Kind,
		Name: destination.Name, UID: destination.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
		moved.OwnerReferences)
	require.Equal(t, string(destination.UID), moved.Annotations[constants.AnnotationOpenBaoOwnerUID])
	require.Equal(t, string(source.UID), moved.Annotations["openbao.org/restore-execution-id"],
		"the execution receipt is history and is not rewritten")
	require.Equal(t, job.Labels, moved.Labels)

	request.Source, request.Destination = destination, source
	require.NoError(t, TransferOwnership(t.Context(), c, request), "the reverse transfer restores the source")

	request.Resource = TransferIdentity{APIVersion: "v1", Kind: "ConfigMap", Namespace: "tenant", Name: "x",
		UID: types.UID("x")}
	require.ErrorContains(t, TransferOwnership(t.Context(), c, request), "unsupported migration resource")
}

func tenantRecord(t *testing.T, mutate func(*api.OpenBaoTenant)) (*unstructured.Unstructured, ImportRequest) {
	t.Helper()
	tenant := &api.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{Name: "team-a", Namespace: "tenant", UID: "tenant-source-uid",
			ResourceVersion: "3", Generation: 1, Finalizers: []string{api.OpenBaoTenantFinalizer},
			Labels: map[string]string{"example.org/team": "a"}},
		Spec:   api.OpenBaoTenantSpec{TargetNamespace: "tenant"},
		Status: api.OpenBaoTenantStatus{Provisioned: true},
	}
	if mutate != nil {
		mutate(tenant)
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tenant)
	require.NoError(t, err)
	source := &unstructured.Unstructured{Object: object}
	source.SetAPIVersion(PlanSourceAPIVersion)
	source.SetKind(discoveryTenantKind)
	return source, ImportRequest{Source: identity(PlanSourceAPIVersion, discoveryTenantKind, source),
		NamespaceUID: "namespace-uid", MigrationID: "migration-test", LabelKeys: []string{"example.org/team"}}
}

func TestImportRecreatesATenantWithoutItsStatus(t *testing.T) {
	source, request := tenantRecord(t, nil)
	statusWrites := 0
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			object.SetUID("tenant-destination-uid")
			return c.Create(ctx, object, opts...)
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch,
			...client.SubResourcePatchOption,
		) error {
			statusWrites++
			return nil
		},
	})
	payload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	require.Nil(t, payload.Status, "the destination provisioner recomputes provisioning")
	require.Equal(t, map[string]any{"targetNamespace": "tenant"}, payload.Object.Object["spec"])

	request.ExpectedImportDigest = payload.Digest
	result, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, result.StatusImported)
	created := &unstructured.Unstructured{}
	created.SetAPIVersion(PlanDestinationAPIVersion)
	created.SetKind(discoveryTenantKind)
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "tenant", Name: "team-a"}, created))
	require.NotContains(t, created.Object, "status")
	require.Empty(t, created.GetFinalizers())
	require.Equal(t, "a", created.GetLabels()["example.org/team"])

	repeated, err := Import(t.Context(), c, request)
	require.NoError(t, err, "a re-run adopts its own import")
	require.Equal(t, result.Destination.UID, repeated.Destination.UID)
	require.Zero(t, statusWrites)
}

func TestTenantRecordsNeedATargetAndNoOwner(t *testing.T) {
	source, _ := tenantRecord(t, nil)
	require.NoError(t, validateTenantSource(source))
	source, _ = tenantRecord(t, func(tenant *api.OpenBaoTenant) { tenant.Spec.TargetNamespace = "" })
	require.ErrorContains(t, validateTenantSource(source), "no target namespace")
	source, _ = tenantRecord(t, func(tenant *api.OpenBaoTenant) {
		tenant.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "x"}}
	})
	require.ErrorContains(t, validateTenantSource(source), "owner references")
}
