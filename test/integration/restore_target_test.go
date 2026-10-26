//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const restoreDestinationApprovalLabel = "openbao.org/restore-target-approved"

func TestVAP_RestoreDestinationApproval(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	setRestoreDestinationApproval(t, namespace, "")

	request := newFreshRestoreRequest(namespace, "approved-target")
	// An object label cannot substitute for the platform's namespace approval.
	request.Labels = map[string]string{restoreDestinationApprovalLabel: "true"}
	for _, lifecycle := range []api.RestoreTargetLifecycle{api.RestoreTargetLifecycleRetain, api.RestoreTargetLifecycleDisposable} {
		request.Spec.TargetLifecycle = lifecycle
		for _, actor := range []client.Client{k8sClient, newControllerClient(t)} {
			err := actor.Create(ctx, request.DeepCopy(), client.DryRunAll)
			requireAdmissionDenied(t, err)
			require.ErrorContains(t, err, restoreDestinationApprovalLabel)
		}
	}

	username := "restore-destination-requester"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	err := newImpersonatedClient(t, username).Patch(ctx, ns, client.RawPatch(types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"openbao.org/restore-target-approved":"true"}}}`)))
	require.True(t, apierrors.IsForbidden(err), "restore write access must not grant namespace approval")

	// Ordinary cluster creation and restore into an existing target are unchanged.
	require.NoError(t, k8sClient.Create(ctx, newMinimalClusterObj(namespace, "ordinary"), client.DryRunAll))
	legacy := request.DeepCopy()
	legacy.Spec.ClusterTemplate, legacy.Spec.TargetLifecycle = nil, ""
	require.NoError(t, k8sClient.Create(ctx, legacy, client.DryRunAll))

	setRestoreDestinationApproval(t, namespace, "false")
	setRestoreDestinationApproval(t, namespace, "TRUE")
	setRestoreDestinationApproval(t, namespace, "true")
	request.Finalizers = []string{api.OpenBaoRestoreFinalizer}
	require.NoError(t, k8sClient.Create(ctx, request))
	target := newMinimalClusterObj(namespace, request.Spec.Cluster)
	target.Annotations = map[string]string{constants.AnnotationRestoreOrigin: string(request.UID)}
	target.Finalizers = []string{api.OpenBaoClusterFinalizer}
	require.NoError(t, newControllerClient(t).Create(ctx, target))

	setRestoreDestinationApproval(t, namespace, "")
	err = k8sClient.Create(ctx, newFreshRestoreRequest(namespace, "next"), client.DryRunAll)
	require.ErrorContains(t, err, restoreDestinationApprovalLabel)
	// The helper also proves that target creation is denied after revocation,
	// including when its restore request was admitted earlier.
	request.Status.Phase = api.RestorePhaseFailed
	require.NoError(t, newControllerClient(t).Status().Update(ctx, request))
	for _, object := range []client.Object{request, target} {
		before := object.DeepCopyObject().(client.Object)
		annotations := object.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["test/cleanup"] = "allowed"
		object.SetAnnotations(annotations)
		require.NoError(t, newControllerClient(t).Patch(ctx, object, client.MergeFrom(before)))
		require.NoError(t, newControllerClient(t).Delete(ctx, object))
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object))
		before = object.DeepCopyObject().(client.Object)
		object.SetFinalizers(nil)
		require.NoError(t, newControllerClient(t).Patch(ctx, object, client.MergeFrom(before)))
	}
}

func setRestoreDestinationApproval(t *testing.T, namespace, value string) {
	t.Helper()
	ns := &corev1.Namespace{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, ns))
	before := ns.DeepCopy()
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	if value == "" {
		delete(ns.Labels, restoreDestinationApprovalLabel)
	} else {
		ns.Labels[restoreDestinationApprovalLabel] = value
	}
	require.NoError(t, k8sClient.Patch(ctx, ns, client.MergeFrom(before)))

	// Admission observes namespace labels asynchronously. Probe without creating
	// a target, and also wait for the protection policy to become active.
	probe := newMinimalClusterObj(namespace, "approval-probe")
	probe.Annotations = map[string]string{constants.AnnotationRestoreOrigin: "probe"}
	require.Eventually(t, func() bool {
		err := newControllerClient(t).Create(ctx, probe.DeepCopy(), client.DryRunAll)
		if value == "true" {
			return err == nil
		}
		return err != nil && strings.Contains(err.Error(), restoreDestinationApprovalLabel)
	}, 10*time.Second, 100*time.Millisecond)
}

func TestCRD_FreshRestoreOptions(t *testing.T) {
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	for _, tc := range []struct {
		name  string
		edit  func(*api.OpenBaoRestoreSpec)
		valid bool
	}{
		{name: "retained", valid: true},
		{name: "version-271", valid: true, edit: func(s *api.OpenBaoRestoreSpec) {
			s.ClusterTemplate.Version, s.Source.ExpectedVersion = "2.7.1", "2.7.1"
		}},
		{name: "version-272", valid: true, edit: func(s *api.OpenBaoRestoreSpec) {
			s.ClusterTemplate.Version, s.Source.ExpectedVersion = "2.7.2", "2.7.2"
		}},
		{name: "unqualified-patch", edit: func(s *api.OpenBaoRestoreSpec) {
			s.ClusterTemplate.Version, s.Source.ExpectedVersion = "2.7.3", "2.7.3"
		}},
		{name: "disposable", valid: true, edit: func(s *api.OpenBaoRestoreSpec) { s.TargetLifecycle = api.RestoreTargetLifecycleDisposable }},
		{name: "legacy", valid: true, edit: func(s *api.OpenBaoRestoreSpec) {
			s.ClusterTemplate, s.TargetLifecycle, s.Force = nil, "", false
			s.Source.ExpectedClusterID, s.Source.ExpectedVersion = "", ""
		}},
		{name: "missing-lifecycle", edit: func(s *api.OpenBaoRestoreSpec) { s.TargetLifecycle = "" }},
		{name: "missing-template", edit: func(s *api.OpenBaoRestoreSpec) { s.ClusterTemplate = nil }},
		{name: "missing-source-id", edit: func(s *api.OpenBaoRestoreSpec) { s.Source.ExpectedClusterID = "" }},
		{name: "version-mismatch", edit: func(s *api.OpenBaoRestoreSpec) { s.Source.ExpectedVersion = "2.6.3" }},
		{name: "non-force", edit: func(s *api.OpenBaoRestoreSpec) { s.Force = false }},
		{name: "static-auth", edit: func(s *api.OpenBaoRestoreSpec) { s.TokenSecretRef = &corev1.LocalObjectReference{Name: "token"} }},
		{name: "unsupported-version", edit: func(s *api.OpenBaoRestoreSpec) { s.ClusterTemplate.Version = "2.6.3" }},
		{name: "retained-cleanup", edit: func(s *api.OpenBaoRestoreSpec) { s.CleanupAfterSeconds = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := newFreshRestoreRequest(namespace, tc.name)
			if tc.edit != nil {
				tc.edit(&request.Spec)
			}
			err := k8sClient.Create(ctx, request, client.DryRunAll)
			if tc.valid {
				require.NoError(t, err)
			} else {
				requireInvalidRequest(t, err)
			}
		})
	}
}

func TestVAP_GarbageCollectionDoesNotNeedExecutableDelegation(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	username := "system:serviceaccount:kube-system:generic-garbage-collector"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	collector := newImpersonatedClient(t, username)
	cluster := newMinimalClusterObj(namespace, "disposable")
	cluster.Spec.InitContainer = &api.InitContainerConfig{Enabled: true, Image: "example/init:qualification"}
	cluster.Finalizers = []string{"foregroundDeletion"}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	before := cluster.DeepCopy()
	cluster.Annotations = map[string]string{"test": "unchanged-spec"}
	requireAdmissionDenied(t, collector.Patch(ctx, cluster, client.MergeFrom(before), client.DryRunAll))
	require.NoError(t, k8sClient.Delete(ctx, before))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	before = cluster.DeepCopy()
	cluster.Finalizers = nil
	require.NoError(t, collector.Patch(ctx, cluster, client.MergeFrom(before), client.DryRunAll))
	cluster.Spec.InitContainer.Image = "example/changed:qualification"
	requireAdmissionDenied(t, collector.Patch(ctx, cluster, client.MergeFrom(before), client.DryRunAll))
}

func newFreshRestoreRequest(namespace, name string) *api.OpenBaoRestore {
	return &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: api.OpenBaoRestoreSpec{
		Cluster: name, Force: true, TargetLifecycle: api.RestoreTargetLifecycleRetain,
		Source: api.RestoreSource{Key: "snapshot", ExpectedClusterID: "source", ExpectedVersion: "2.7.0",
			Target: api.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example"}},
		ClusterTemplate: &api.RestoreClusterTemplate{Version: "2.7.0", Storage: api.StorageConfig{Size: "1Gi"},
			TLS: api.TLSConfig{Enabled: true, Mode: api.TLSModeOperatorManaged, RotationPeriod: "720h"},
			Unseal: api.UnsealConfig{Type: "transit", Transit: &api.TransitSealConfig{Address: "https://seal.example", MountPath: "transit", KeyName: "recovery"},
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit"}}},
	}}
}
