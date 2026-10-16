//go:build integration

package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/service/networking"
	"github.com/kubebao/openbao-operator/internal/service/provisioner"
)

func ciliumPolicyObject(namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("cilium.io/v2")
	obj.SetKind("CiliumNetworkPolicy")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func TestCiliumAPIServerPolicyLifecycle(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	ensureProvisionerRBACApplied(t)
	provisionerClient := newImpersonatedClient(t, provisionerUsername)
	applyClientObject(t, provisionerClient, provisioner.GenerateTenantRole(namespace))
	applyClientObject(t, provisionerClient, provisioner.GenerateTenantRoleBinding(namespace, provisioner.OperatorServiceAccount{Name: testControllerSAName, Namespace: testDefaultOperatorNS}))
	// The delegated permission is limited to namespaced CNPs in this tenant.
	for _, target := range []struct{ namespace, resource string }{
		{newTestNamespace(t), "ciliumnetworkpolicies"}, {"", "ciliumclusterwidenetworkpolicies"},
	} {
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: controllerUsername, ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: target.namespace, Group: "cilium.io", Resource: target.resource, Verb: "create",
			},
		}}
		require.NoError(t, k8sClient.Create(ctx, review))
		require.False(t, review.Status.Allowed, "unexpected Cilium privilege: %#v", target)
	}
	cluster := newMinimalClusterObj(namespace, "native-api")
	cluster.Spec.Network = &api.NetworkConfig{APIServerCIDR: "10.0.0.1/32", APIServerEgress: &api.APIServerEgressConfig{Mode: "Cilium"}}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	require.EqualValues(t, 6443, cluster.Spec.Network.APIServerEgress.Port)
	controller := newImpersonatedClient(t, controllerUsername)
	manager := networking.NewManagerWithReader(controller, controller, k8sScheme, "openbao-operator-system", constants.PlatformKubernetes)
	// Wait for admission to observe the installed policies, then exercise SSA as
	// the real controller identity rather than the envtest administrator.
	require.Eventually(t, func() bool { return manager.Reconcile(ctx, logr.Discard(), cluster) == nil }, 5*time.Second, 100*time.Millisecond)
	policy := ciliumPolicyObject(namespace, cluster.Name+"-api-server")
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))
	require.Equal(t, string(cluster.UID), policy.GetAnnotations()[constants.AnnotationOpenBaoOwnerUID])
	require.Equal(t, cluster.UID, policy.GetOwnerReferences()[0].UID)
	uid := policy.GetUID()
	got := manager.EvaluateAPIServerNetworkReadiness(ctx, logr.Discard(), cluster)
	require.Equal(t, metav1.ConditionTrue, got.Status, got.Message)
	// A platform-owned policy is neither adopted nor deleted during opt-out.
	external := ciliumPolicyObject(namespace, "administrator-api-server")
	external.Object["spec"] = map[string]interface{}{"endpointSelector": map[string]interface{}{}}
	require.NoError(t, k8sClient.Create(ctx, external))
	cluster.Spec.Network.APIServerEgress.Port = 7443
	require.NoError(t, k8sClient.Update(ctx, cluster))
	require.NoError(t, manager.Reconcile(ctx, logr.Discard(), cluster))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))
	require.Equal(t, uid, policy.GetUID())
	egress, _, err := unstructured.NestedSlice(policy.Object, "spec", "egress")
	require.NoError(t, err)
	require.Contains(t, egress[0], "toEntities")
	require.Contains(t, policy.Object["spec"], "enableDefaultDeny")
	// Direct edits, label removal, forged provenance, and adoption are denied.
	user := newPrivilegedImpersonatedClient(t, "cilium-test-user")
	before := policy.DeepCopy()
	policy.SetLabels(nil)
	requireAdmissionDenied(t, user.Patch(ctx, policy, client.MergeFrom(before)))
	requireAdmissionDenied(t, user.Delete(ctx, before))
	takeover := external.DeepCopy()
	takeover.SetLabels(before.GetLabels())
	takeover.SetOwnerReferences(before.GetOwnerReferences())
	takeover.SetAnnotations(before.GetAnnotations())
	requireAdmissionDenied(t, controller.Patch(ctx, takeover, client.MergeFrom(external)))
	rogue := ciliumPolicyObject(namespace, "rogue-api-server")
	rogue.Object["spec"] = external.Object["spec"]
	requireAdmissionDenied(t, controller.Create(ctx, rogue))
	cluster.Spec.Network.APIServerEgress = nil
	require.NoError(t, k8sClient.Update(ctx, cluster))
	// A controller upgraded before RBAC must retry cleanup after permissions return.
	legacyRole := provisioner.GenerateTenantRole(namespace)
	legacyRole.Rules = slices.DeleteFunc(legacyRole.Rules, func(rule rbacv1.PolicyRule) bool {
		return slices.Contains(rule.APIGroups, "cilium.io")
	})
	applyClientObject(t, provisionerClient, legacyRole)
	require.Eventually(t, func() bool {
		return apierrors.IsForbidden(controller.Get(ctx, client.ObjectKeyFromObject(before), policy))
	}, 5*time.Second, 100*time.Millisecond)
	err = manager.Reconcile(ctx, logr.Discard(), cluster)
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	require.NotErrorIs(t, err, operatorerrors.ErrPermanentConfig)
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(before), policy))
	applyClientObject(t, provisionerClient, provisioner.GenerateTenantRole(namespace))
	require.Eventually(t, func() bool {
		return controller.Get(ctx, client.ObjectKeyFromObject(before), policy) == nil
	}, 5*time.Second, 100*time.Millisecond)
	require.NoError(t, manager.Reconcile(ctx, logr.Discard(), cluster))
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(before), before)))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(external), external))
}

func TestCiliumAPIServerConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		port       int32
	}{
		{"mode", "Other", 6443}, {"negative", "Cilium", -1}, {"overflow", "Cilium", 65536}, {"missing-mode", "", 6443},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "invalid-native-api")
			cluster.Spec.Network = &api.NetworkConfig{APIServerEgress: &api.APIServerEgressConfig{Mode: tc.mode, Port: tc.port}}
			err := k8sClient.Create(ctx, cluster)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
		})
	}
}
