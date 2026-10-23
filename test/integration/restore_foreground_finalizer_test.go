//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	restoremanager "github.com/kubebao/openbao-operator/internal/service/restore"
)

const restoreAuthorityMessage = "Users creating or updating an OpenBaoRestore must be authorized " +
	"to restore the target OpenBaoCluster."

func TestRestoreForegroundFinalization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)
	installRestoreExecutionPolicy(t)
	const (
		username        = "restore-garbage-collector"
		changedMetadata = "changed"
	)
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	updater := newImpersonatedClient(t, username)
	access := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: username, ResourceAttributes: &authorizationv1.ResourceAttributes{
			Group: "openbao.org", Resource: "openbaoclusters", Namespace: namespace, Name: "missing-target", Verb: "restore",
		},
	}}
	require.NoError(t, k8sClient.Create(ctx, access))
	require.False(t, access.Status.Allowed, "ordinary update access must not include destructive restore authority")

	// The real deletion handler releases unsubmitted work without changing its
	// phase. Garbage collection must also finish these nonterminal deletions.
	for _, phase := range []openbaov1alpha1.RestorePhase{
		openbaov1alpha1.RestorePhaseFailed, openbaov1alpha1.RestorePhaseCompleted,
		openbaov1alpha1.RestorePhasePending, openbaov1alpha1.RestorePhaseValidating,
		openbaov1alpha1.RestorePhaseRunning, "",
	} {
		name := string(phase)
		if name == "" {
			name = "DefaultedPending"
		}
		t.Run(name, func(t *testing.T) {
			request := newForegroundRestore(t, namespace, "phase-"+strings.ToLower(string(phase))+"-cleanup", phase, nil)
			effectivePhase := request.Status.Phase
			beginForegroundRestoreDeletion(t, request, true)
			require.Equal(t, effectivePhase, request.Status.Phase, "cleanup must preserve the recorded phase")
			require.Equal(t, []string{metav1.FinalizerDeleteDependents}, request.Finalizers)
			request.Finalizers = nil
			require.NoError(t, updater.Update(ctx, request), "ordinary UPDATE must finish controller-approved cleanup")
			require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request)))
		})
	}

	t.Run("optional metadata absent", func(t *testing.T) {
		request := foregroundRestore(namespace, "optional-metadata-absent")
		request.Annotations, request.Labels, request.OwnerReferences = nil, nil, nil
		require.NoError(t, k8sClient.Create(ctx, request))
		beginForegroundRestoreDeletion(t, request, true)
		request.Finalizers = nil
		require.NoError(t, updater.Update(ctx, request))
		require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request)))
	})

	t.Run("other changes retain restore authority", func(t *testing.T) {
		request := newForegroundRestore(t, namespace, "protected-metadata", openbaov1alpha1.RestorePhaseUnknown,
			&openbaov1alpha1.RestoreExecutionStatus{Stage: openbaov1alpha1.RestoreExecutionStagePrepared, JobUID: "bound-job"})
		beginForegroundRestoreDeletion(t, request, true)
		for _, test := range []struct {
			name   string
			change func(*openbaov1alpha1.OpenBaoRestore)
		}{
			{"spec", func(r *openbaov1alpha1.OpenBaoRestore) { r.Spec.Source.Key = "other-snapshot" }},
			{"resume-acknowledgement", func(r *openbaov1alpha1.OpenBaoRestore) {
				r.Annotations[constants.AnnotationRestoreAcknowledge] = string(r.UID) + "/Resume"
			}},
			{"abandon-acknowledgement", func(r *openbaov1alpha1.OpenBaoRestore) {
				r.Annotations[constants.AnnotationRestoreAcknowledge] = string(r.UID) + "/Abandon"
			}},
			{"annotation", func(r *openbaov1alpha1.OpenBaoRestore) { r.Annotations["example.com/preserve"] = changedMetadata }},
			{"annotation-removal", func(r *openbaov1alpha1.OpenBaoRestore) { r.Annotations = nil }},
			{"label", func(r *openbaov1alpha1.OpenBaoRestore) { r.Labels["example.com/preserve"] = changedMetadata }},
			{"label-removal", func(r *openbaov1alpha1.OpenBaoRestore) { r.Labels = nil }},
			{"owner-reference", func(r *openbaov1alpha1.OpenBaoRestore) { r.OwnerReferences[0].UID = "other-owner" }},
			{"owner-reference-removal", func(r *openbaov1alpha1.OpenBaoRestore) { r.OwnerReferences = nil }},
			{"generate-name", func(r *openbaov1alpha1.OpenBaoRestore) { r.GenerateName = "other-" }},
			{"unchanged-finalizers", func(r *openbaov1alpha1.OpenBaoRestore) { r.Finalizers = request.Finalizers }},
		} {
			t.Run(test.name, func(t *testing.T) {
				changed := request.DeepCopy()
				changed.Finalizers = nil
				test.change(changed)
				err := updater.Update(ctx, changed, client.DryRunAll)
				if test.name == "spec" {
					require.ErrorContains(t, err, "spec is immutable")
				} else {
					require.ErrorContains(t, err, restoreAuthorityMessage)
				}
			})
		}
		// Resume and Abandon are valid acknowledgements in this state. Their
		// rejection above therefore comes from the destructive authority check.
		for _, acknowledgement := range []string{"Resume", "Abandon"} {
			changed := request.DeepCopy()
			changed.Annotations[constants.AnnotationRestoreAcknowledge] = string(changed.UID) + "/" + acknowledgement
			require.NoError(t, k8sClient.Update(ctx, changed, client.DryRunAll))
		}
	})

	t.Run("main resource preserves status", func(t *testing.T) {
		request := newForegroundRestore(t, namespace, "status-preserved", openbaov1alpha1.RestorePhaseFailed, nil)
		beginForegroundRestoreDeletion(t, request, true)
		request.Finalizers = nil
		request.Status.Phase = openbaov1alpha1.RestorePhaseCompleted
		// The API server restores stored status before main-resource admission.
		// Direct CEL tests separately reject an actual status delta.
		require.NoError(t, updater.Update(ctx, request, client.DryRunAll))
		require.Equal(t, openbaov1alpha1.RestorePhaseFailed, request.Status.Phase)
	})

	t.Run("operator finalizer still protects running work", func(t *testing.T) {
		request := newForegroundRestore(t, namespace, "operator-protected", openbaov1alpha1.RestorePhaseRunning, nil)
		beginForegroundRestoreDeletion(t, request, false)
		request.Finalizers = nil
		require.ErrorContains(t, updater.Update(ctx, request, client.DryRunAll), restoreAuthorityMessage)
	})

	t.Run("other finalizer is protected", func(t *testing.T) {
		request := newForegroundRestore(t, namespace, "other-protected", openbaov1alpha1.RestorePhaseFailed, nil)
		before := request.DeepCopy()
		request.Finalizers = append(request.Finalizers, "example.com/protect")
		require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
		beginForegroundRestoreDeletion(t, request, true)
		request.Finalizers = nil
		require.ErrorContains(t, updater.Update(ctx, request, client.DryRunAll), restoreAuthorityMessage)
	})

	t.Run("nondeleting object is protected", func(t *testing.T) {
		request := newForegroundRestore(t, namespace, "not-deleting", openbaov1alpha1.RestorePhaseFailed, nil)
		before := request.DeepCopy()
		request.Finalizers = []string{metav1.FinalizerDeleteDependents}
		require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
		request.Finalizers = nil
		require.ErrorContains(t, updater.Update(ctx, request, client.DryRunAll), restoreAuthorityMessage)
	})

	t.Run("create still requires restore authority", func(t *testing.T) {
		request := foregroundRestore(namespace, "ordinary-create")
		require.ErrorContains(t, updater.Create(ctx, request, client.DryRunAll), restoreAuthorityMessage)
	})
}

func foregroundRestore(namespace, name string) *openbaov1alpha1.OpenBaoRestore {
	return &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
			Labels:      map[string]string{"example.com/preserve": "original"},
			Annotations: map[string]string{"example.com/preserve": "original"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "original-owner",
			}},
			Finalizers: []string{openbaov1alpha1.OpenBaoRestoreFinalizer}},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "missing-target", Source: openbaov1alpha1.RestoreSource{
			Key:    "snapshot",
			Target: openbaov1alpha1.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example.com"}}},
	}
}

func newForegroundRestore(
	t *testing.T, namespace, name string,
	phase openbaov1alpha1.RestorePhase, execution *openbaov1alpha1.RestoreExecutionStatus,
) *openbaov1alpha1.OpenBaoRestore {
	t.Helper()
	request := foregroundRestore(namespace, name)
	require.NoError(t, k8sClient.Create(ctx, request))
	request.Status.Phase, request.Status.Execution = phase, execution
	require.NoError(t, newControllerClient(t).Status().Update(ctx, request))
	return request
}

func beginForegroundRestoreDeletion(t *testing.T, request *openbaov1alpha1.OpenBaoRestore, cleanup bool) {
	t.Helper()
	require.NoError(t, k8sClient.Delete(ctx, request, client.PropagationPolicy(metav1.DeletePropagationForeground)))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.NotNil(t, request.DeletionTimestamp)
	if cleanup {
		controller := newControllerClient(t)
		manager := restoremanager.NewManager(controller, k8sScheme, nil, nil, "").WithReader(controller)
		_, err := manager.Reconcile(ctx, logr.Discard(), request)
		require.NoError(t, err)
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
		require.NotContains(t, request.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
	}
}
