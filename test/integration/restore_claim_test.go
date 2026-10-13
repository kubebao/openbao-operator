//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	restoremanager "github.com/kubebao/openbao-operator/internal/service/restore"
)

func installRestoreExecutionPolicy(t *testing.T) {
	t.Helper()
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	for name, object := range map[string]client.Object{
		"openbao-protect-restore-execution.yaml":         policy,
		"openbao-protect-restore-execution-binding.yaml": binding,
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", name))
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(data, object))
		require.NoError(t, k8sClient.Create(ctx, object))
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), object) })
	}
}

func TestRestoreClaimAdmissionAndConcurrency(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	request := newClaimRequest(t, namespace, "competing")
	beforeAck := request.DeepCopy()
	request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Resume"}
	require.Eventually(t, func() bool {
		return k8sClient.Patch(ctx, request, client.MergeFrom(beforeAck), client.DryRunAll) != nil
	}, 10*time.Second, 100*time.Millisecond)
	request = beforeAck
	claim := openbaov1alpha1.RestoreSubmissionClaim{
		PodUID: "executor-pod", TargetPodName: "target-0", TargetPodUID: "voter-pod",
		TargetPodIP: "10.1.2.3", TargetContainerID: "containerd://original", Digest: "sha256:" + strings.Repeat("a", 64), Size: 123,
	}
	helperConfig := rest.CopyConfig(cfg)
	helperConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + namespace + ":target-restore-serviceaccount",
		Extra:    map[string][]string{"authentication.kubernetes.io/pod-uid": {string(claim.PodUID)}},
	}
	helper, err := client.NewWithWatch(helperConfig, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: namespace}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores"}, Verbs: []string{"get"}},
		{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores/status"}, Verbs: []string{"patch"}},
	}}
	require.NoError(t, k8sClient.Create(ctx, role))
	require.NoError(t, k8sClient.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: helperConfig.Impersonate.UserName, APIGroup: rbacv1.GroupName}},
	}))
	// Wait for admission using a dry-run forged status write. A successful dry
	// run is harmless while the API server observes the policy and binding.
	require.Eventually(t, func() bool {
		before := request.DeepCopy()
		forged := request.DeepCopy()
		forged.Status.Phase = openbaov1alpha1.RestorePhaseCompleted
		err := helper.Status().Patch(ctx, forged, client.MergeFrom(before), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "executor can only append")
	}, 10*time.Second, 100*time.Millisecond)
	var winners atomic.Int32
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			<-start
			if err := restoremanager.ClaimRestore(ctx, helper, client.ObjectKeyFromObject(request), request.UID, claim); err == nil {
				winners.Add(1)
			} else {
				t.Logf("claim rejected: %v", err)
			}
		})
	}
	close(start)
	workers.Wait()
	require.Equal(t, int32(1), winners.Load(), "only one invocation may proceed to POST")
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
	require.NotNil(t, request.Status.SubmissionClaim)
	before := request.DeepCopy()
	request.Status.SubmissionClaim = nil
	requireAdmissionDenied(t, k8sClient.Status().Patch(ctx, request, client.MergeFrom(before)))
	requireAdmissionDenied(t, k8sClient.Delete(ctx, before))
	// Acknowledgement does not permit rewriting the execution claim.
	request = before.DeepCopy()
	request.Status.AdministratorDisposition = openbaov1alpha1.RestoreAdministratorAbandon
	requireAdmissionDenied(t, k8sClient.Status().Patch(ctx, request, client.MergeFrom(before)))
	require.NoError(t, newControllerClient(t).Status().Patch(ctx, request, client.MergeFrom(before)))
	require.NoError(t, k8sClient.Delete(ctx, request))

	lost := newClaimRequest(t, namespace, "lost-response")
	lostResponse := errors.New("API response lost after applying claim")
	uncertain := interceptor.NewClient(helper, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if err := c.SubResource(subresource).Patch(ctx, obj, patch, opts...); err != nil {
				return err
			}
			return lostResponse
		},
	})
	require.ErrorIs(t, restoremanager.ClaimRestore(ctx, uncertain, client.ObjectKeyFromObject(lost), lost.UID, claim), lostResponse)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(lost), lost))
	require.NotNil(t, lost.Status.SubmissionClaim)
	require.Error(t, restoremanager.ClaimRestore(ctx, helper, client.ObjectKeyFromObject(lost), lost.UID, claim), "a restarted invocation cannot reuse the stored claim")

	cluster := createMinimalCluster(t, namespace, "target")
	original := cluster.DeepCopy()
	cluster.Annotations = map[string]string{constants.AnnotationRestoreHold: string(lost.UID)}
	requireAdmissionDenied(t, k8sClient.Patch(ctx, cluster, client.MergeFrom(original)))
	require.NoError(t, newControllerClient(t).Patch(ctx, cluster, client.MergeFrom(original)))
	requireAdmissionDenied(t, k8sClient.Delete(ctx, cluster))
	original = cluster.DeepCopy()
	delete(cluster.Annotations, constants.AnnotationRestoreHold)
	requireAdmissionDenied(t, k8sClient.Patch(ctx, cluster, client.MergeFrom(original)))
	require.NoError(t, newControllerClient(t).Patch(ctx, cluster, client.MergeFrom(original)))

	t.Run("failed before submission permits deletion", func(t *testing.T) {
		failed := newClaimRequest(t, namespace, "failed-before-claim")
		failed.Status.Phase = openbaov1alpha1.RestorePhaseFailed
		failed.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageTerminalObserved
		failed.Status.Execution.TerminalResult = openbaov1alpha1.RestoreExecutionResultFailed
		require.NoError(t, k8sClient.Status().Update(ctx, failed))
		require.NoError(t, k8sClient.Delete(ctx, failed))
	})

	t.Run("blocked disposable cleanup permits administrator handoff", func(t *testing.T) {
		verifyBlockedCleanupHandoff(t, namespace)
	})
}

func verifyBlockedCleanupHandoff(t *testing.T, namespace string) {
	t.Helper()
	request := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "blocked-cleanup", Namespace: namespace,
			Finalizers: []string{openbaov1alpha1.OpenBaoRestoreFinalizer}},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "missing-original", TargetLifecycle: openbaov1alpha1.RestoreTargetLifecycleDisposable, Force: true,
			Source: openbaov1alpha1.RestoreSource{Key: "snapshot", ExpectedClusterID: "source", ExpectedVersion: "2.7.0",
				Target: openbaov1alpha1.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example"}},
			ClusterTemplate: &openbaov1alpha1.RestoreClusterTemplate{Version: "2.7.0", Storage: openbaov1alpha1.StorageConfig{Size: "1Gi"},
				TLS: openbaov1alpha1.TLSConfig{Enabled: true, Mode: openbaov1alpha1.TLSModeOperatorManaged, RotationPeriod: "720h"},
				Unseal: openbaov1alpha1.UnsealConfig{Type: "transit", Transit: &openbaov1alpha1.TransitSealConfig{Address: "https://seal.example", MountPath: "transit", KeyName: "recovery"},
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit"}}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, request))
	setRestoreDestinationApproval(t, namespace, "")
	request.Status = openbaov1alpha1.OpenBaoRestoreStatus{Phase: openbaov1alpha1.RestorePhaseUnknown,
		Target: &openbaov1alpha1.RestoreTargetStatus{ReservedAt: metav1.Now(), UID: "original", Cleanup: openbaov1alpha1.RestoreTargetCleanupFailed}}
	require.NoError(t, k8sClient.Status().Update(ctx, request))
	before := request.DeepCopy()
	request.Finalizers = nil
	requireAdmissionDenied(t, newControllerClient(t).Patch(ctx, request, client.MergeFrom(before), client.DryRunAll))
	request = before.DeepCopy()
	request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Resume"}
	requireAdmissionDenied(t, k8sClient.Patch(ctx, request, client.MergeFrom(before), client.DryRunAll))
	request.Annotations[constants.AnnotationRestoreAcknowledge] = string(request.UID) + "/Abandon"
	require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
	manager := restoremanager.NewManager(newControllerClient(t), k8sScheme, nil, nil, "")
	_, err := manager.Reconcile(ctx, logr.Discard(), request)
	require.NoError(t, err)
	require.Equal(t, openbaov1alpha1.RestoreAdministratorAbandon, request.Status.AdministratorDisposition)
	require.Equal(t, openbaov1alpha1.RestoreTargetCleanupFailed, request.Status.Target.Cleanup)
	require.NoError(t, k8sClient.Delete(ctx, request))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
	_, err = manager.Reconcile(ctx, logr.Discard(), request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request)))
}

func newClaimRequest(t *testing.T, namespace, name string, accounts ...string) *openbaov1alpha1.OpenBaoRestore {
	t.Helper()
	request := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "target", Source: openbaov1alpha1.RestoreSource{
			Key: "snapshot", Target: openbaov1alpha1.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example.com"},
		}},
	}
	if len(accounts) != 0 {
		request.Spec.ServiceAccountName = accounts[0]
	}
	require.NoError(t, k8sClient.Create(ctx, request))
	now := metav1.Now()
	request.Status = openbaov1alpha1.OpenBaoRestoreStatus{
		Phase:     openbaov1alpha1.RestorePhaseRunning,
		Execution: &openbaov1alpha1.RestoreExecutionStatus{OperationID: string(request.UID), Stage: openbaov1alpha1.RestoreExecutionStageCreated, JobName: "restore", JobUID: "job", PreparedAt: &now},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, request))
	return request
}

func TestRestoreResumePrerequisitesAndMissingTargetFeedback(t *testing.T) {
	installRestoreExecutionPolicy(t)
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	for _, state := range []string{"disposable", "no-job-uid", "no-execution", "failed-cleanup", "missing-target", "replaced-target"} {
		t.Run(state, func(t *testing.T) {
			request := newFreshRestoreRequest(namespace, state)
			request.Spec.TargetLifecycle = openbaov1alpha1.RestoreTargetLifecycleRetain
			if state == "disposable" {
				request.Spec.TargetLifecycle = openbaov1alpha1.RestoreTargetLifecycleDisposable
			}
			require.NoError(t, k8sClient.Create(ctx, request))
			request.Status = openbaov1alpha1.OpenBaoRestoreStatus{
				Phase:  openbaov1alpha1.RestorePhaseUnknown,
				Target: &openbaov1alpha1.RestoreTargetStatus{UID: "original-target", ReservedAt: metav1.Now()},
				Execution: &openbaov1alpha1.RestoreExecutionStatus{OperationID: string(request.UID),
					TargetUID: "original-target", JobName: "restore", JobUID: "job", Stage: openbaov1alpha1.RestoreExecutionStageCreated},
			}
			switch state {
			case "no-job-uid":
				request.Status.Execution.JobUID = ""
			case "no-execution":
				request.Status.Execution = nil
			case "failed-cleanup":
				request.Status.Target.Cleanup = openbaov1alpha1.RestoreTargetCleanupFailed
			case "replaced-target":
				createMinimalCluster(t, namespace, request.Spec.Cluster)
			}
			require.NoError(t, k8sClient.Status().Update(ctx, request))
			before := request.DeepCopy()
			request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Resume"}
			if state != "missing-target" && state != "replaced-target" {
				require.Eventually(t, func() bool {
					err := k8sClient.Patch(ctx, request, client.MergeFrom(before), client.DryRunAll)
					return err != nil && strings.Contains(err.Error(), "otherwise use Abandon")
				}, 10*time.Second, 100*time.Millisecond)
				request.Annotations[constants.AnnotationRestoreAcknowledge] = string(request.UID) + "/Abandon"
				require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before), client.DryRunAll))
				return
			}

			// CEL cannot read the live target. Reconciliation must persist the
			// refusal so administrators do not need controller logs for guidance.
			require.NoError(t, k8sClient.Patch(ctx, request, client.MergeFrom(before)))
			manager := restoremanager.NewManager(newControllerClient(t), k8sScheme, nil, nil, "")
			_, err := manager.Reconcile(ctx, logr.Discard(), request)
			require.NoError(t, err)
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(request), request))
			require.Equal(t, openbaov1alpha1.RestorePhaseUnknown, request.Status.Phase)
			require.Contains(t, request.Status.Message, "use Abandon")
			require.Empty(t, request.Status.AdministratorDisposition)
		})
	}
}
