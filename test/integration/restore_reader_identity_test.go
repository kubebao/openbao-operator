//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	restoremanager "github.com/kubebao/openbao-operator/internal/service/restore"
)

func TestRestoreReaderAccountClaimRemainsPodBoundAndRequestScoped(t *testing.T) {
	installRestoreExecutionPolicy(t)
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	for name, obj := range map[string]client.Object{
		"openbao-restrict-controller-rbac.yaml":         policy,
		"openbao-restrict-controller-rbac-binding.yaml": binding,
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", name))
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(data, obj))
		require.NoError(t, k8sClient.Create(ctx, obj))
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), obj) })
	}
	namespace := newTestNamespace(t)
	request := newClaimRequest(t, namespace, "reader-claim", "snapshot-reader")
	other := newClaimRequest(t, namespace, "other-request")
	cluster := createMinimalCluster(t, namespace, "target")
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "target-restore-serviceaccount-role", Namespace: namespace,
		Labels: map[string]string{"openbao.org/component": "restore"}}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores"}, ResourceNames: []string{request.Name}, Verbs: []string{"get"}},
		{APIGroups: []string{"openbao.org"}, Resources: []string{"openbaorestores/status"}, ResourceNames: []string{request.Name}, Verbs: []string{"patch"}},
	}}
	controller := newPrivilegedImpersonatedClient(t, controllerUsername)
	require.NoError(t, controller.Create(ctx, role))
	grant := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "target-restore-serviceaccount-rolebinding", Namespace: namespace,
		Labels:          map[string]string{"openbao.org/component": "restore", "openbao.org/cluster": cluster.Name},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "openbao.org/v1alpha1", Kind: "OpenBaoCluster", Name: cluster.Name, UID: cluster.UID, Controller: boolPtr(true)}},
	}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: namespace, Name: request.Spec.ServiceAccountName}},
	}
	require.NoError(t, controller.Create(ctx, grant))
	invalid := grant.DeepCopy()
	invalid.Name = "unrelated-restore-serviceaccount-rolebinding"
	require.Eventually(t, func() bool { return controller.Create(ctx, invalid.DeepCopy(), client.DryRunAll) != nil }, 10*time.Second, 100*time.Millisecond)
	helperConfig := rest.CopyConfig(cfg)
	helperConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + namespace + ":snapshot-reader"}
	helper, err := client.New(helperConfig, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return helper.Get(ctx, client.ObjectKeyFromObject(request), &api.OpenBaoRestore{}) == nil }, 10*time.Second, 100*time.Millisecond)
	require.True(t, apierrors.IsForbidden(helper.Get(ctx, client.ObjectKeyFromObject(other), &api.OpenBaoRestore{})))
	claim := api.RestoreSubmissionClaim{PodUID: "reader-pod", TargetPodName: "target-0", TargetPodUID: "voter", TargetPodIP: "10.1.2.3",
		TargetContainerID: "containerd://original", Digest: "sha256:" + strings.Repeat("a", 64), Size: 123}
	claim.ClaimedAt = metav1.Now()
	require.Eventually(t, func() bool {
		unbound := request.DeepCopy()
		unbound.Status.SubmissionClaim = &claim
		err := helper.Status().Patch(ctx, unbound, client.MergeFrom(request), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "executor can only append")
	}, 10*time.Second, 100*time.Millisecond)
	helperConfig.Impersonate.Extra = map[string][]string{"authentication.kubernetes.io/pod-uid": {string(claim.PodUID)}}
	helper, err = client.New(helperConfig, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)
	forged := request.DeepCopy()
	forged.Status.Phase = api.RestorePhaseCompleted
	requireAdmissionDenied(t, helper.Status().Patch(ctx, forged, client.MergeFrom(request), client.DryRunAll))
	require.NoError(t, restoremanager.ClaimRestore(ctx, helper, client.ObjectKeyFromObject(request), request.UID, claim))
	require.Error(t, restoremanager.ClaimRestore(ctx, helper, client.ObjectKeyFromObject(request), request.UID, claim))
}

func TestRestoreReaderAccountRequiresDestinationUseAuthority(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	sourceNS, destination := newTestNamespace(t), newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, sourceNS)
	setRestoreDestinationApproval(t, destination, "true")
	username := "reader-configurator"
	user := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, sourceNS, username)
	grantNamespacedResourceVerbs(t, destination, username, "restore-create", "openbao.org", "openbaorestores", nil, "create")
	grantNamespacedResourceVerbs(t, destination, username, "target-create", "openbao.org", "openbaoclusters", nil, "create", "restore")
	grantNamespacedResourceVerbs(t, destination, username, "unseal-read", "", "secrets", []string{"transit"}, "get")
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "snapshot-reader", Namespace: destination}}
	require.NoError(t, k8sClient.Create(ctx, account))
	source := newMinimalClusterObj(sourceNS, "reader-source")
	source.Spec.InitContainer = nil
	source.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 * * *", JWTAuthRole: "backup",
		Target: api.BackupTarget{Endpoint: "https://storage.example", Bucket: "snapshots"},
		RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: destination,
			ServiceAccountName: account.Name,
			ClusterTemplate:    *newFreshRestoreRequest(destination, "template").Spec.ClusterTemplate,
		},
	}
	require.Eventually(t, func() bool {
		err := user.Create(ctx, source.DeepCopy(), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "Restore test executors require use authority")
	}, 10*time.Second, 100*time.Millisecond)
	grantNamespacedResourceVerbs(t, destination, username, "reader-use", "", "serviceaccounts", []string{account.Name}, "use")
	require.Eventually(t, func() bool { return user.Create(ctx, source.DeepCopy(), client.DryRunAll) == nil }, 10*time.Second, 100*time.Millisecond)

	request := &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: "reader-request", Namespace: destination},
		Spec: api.OpenBaoRestoreSpec{Cluster: "target", ServiceAccountName: "unapproved-reader",
			Source: api.RestoreSource{Key: "snapshot", Target: source.Spec.Backup.Target}},
	}
	require.ErrorContains(t, user.Create(ctx, request.DeepCopy(), client.DryRunAll), "Restore executors require use authority")
	request.Spec.ServiceAccountName = account.Name
	require.NoError(t, user.Create(ctx, request.DeepCopy(), client.DryRunAll))

	source.Spec.Backup.RestoreTest.RoleARN = "arn:aws:iam::123456789012:role/snapshot-reader"
	require.ErrorContains(t, user.Create(ctx, source.DeepCopy(), client.DryRunAll), "Restore test workload identities require destination cloud identity delegation")
	grantNamespacedResourceVerbs(t, destination, username, "reader-cloud", "openbao.org", "openbaoclusters", nil, "usecloudidentities")
	require.Eventually(t, func() bool { return user.Create(ctx, source.DeepCopy(), client.DryRunAll) == nil }, 10*time.Second, 100*time.Millisecond)
}
