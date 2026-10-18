//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
)

func TestE2EFixtureJWTExpiryRequiresActualRejectionAfterLeeway(t *testing.T) {
	statuses := []int{http.StatusOK, http.StatusBadRequest, http.StatusForbidden, http.StatusInternalServerError}
	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/auth/jwt-operator/login", r.URL.Path)
		var payload map[string]string
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "expired-jwt", payload["jwt"])
		assert.Equal(t, "openbao-operator", payload["role"])
		w.WriteHeader(statuses[next])
		next++
	}))
	defer server.Close()
	a := &controllerJWTAPI{ctx: t.Context(), address: server.URL, http: server.Client()}
	for _, test := range []struct {
		name     string
		rejected bool
		wantErr  bool
	}{
		{name: "still accepted during validation leeway"},
		{name: "rejected after leeway", rejected: true},
		{name: "forbidden credential", rejected: true},
		{name: "server error cannot prove expiry", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected, err := a.jwtRejected("expired-jwt")
			require.Equal(t, test.rejected, rejected)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestE2EFixtureCleanupPreservationIsSharedBetweenWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup-state")
	require.NoError(t, os.WriteFile(path, []byte("cleanup\n"), 0o600))
	require.False(t, cleanupPreservationRequested(path))
	// The second worker records a failed cleanup. Node 1 observes the request
	// through the shared file, independently of its process-local skipCleanup.
	require.NoError(t, requestCleanupPreservation(path))
	require.True(t, cleanupPreservationRequested(path))
	require.NoError(t, os.Remove(path))
	require.True(t, cleanupPreservationRequested(path), "unreadable state must preserve fixture evidence")
	otherRun := filepath.Join(t.TempDir(), "cleanup-state")
	require.NoError(t, os.WriteFile(otherRun, []byte("cleanup\n"), 0o600))
	require.False(t, cleanupPreservationRequested(otherRun), "a later run has independent cleanup state")
}

func TestE2EFixtureControllerJWTUsesRealIssuerExpiration(t *testing.T) {
	RegisterTestingT(t)
	expires := metav1.NewTime(time.Now().Add(10 * time.Minute))
	c := kubernetesfake.NewClientset()
	c.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		require.Equal(t, "token", action.GetSubresource())
		request := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenRequest)
		require.Equal(t, int64(600), *request.Spec.ExpirationSeconds)
		require.Equal(t, []string{"target-audience"}, request.Spec.Audiences)
		require.Equal(t, "controller-pod", request.Spec.BoundObjectRef.Name)
		require.Equal(t, "controller-uid", string(request.Spec.BoundObjectRef.UID))
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
			Token: "fixture-token", ExpirationTimestamp: expires,
		}}, nil
	})
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "controller-pod", Namespace: "operator", UID: "controller-uid"},
		Spec: corev1.PodSpec{ServiceAccountName: "controller"}}
	credential := controllerJWTCredentialFor(t.Context(), c, pod, "target-audience")
	require.Equal(t, expires, credential.ExpirationTimestamp, "the expiry wait must use the issuer's actual deadline")
	require.Equal(t, "fixture-token", credential.Token)
}

func TestE2EFixtureRestoreRequestsOwnTheirInputs(t *testing.T) {
	snapshot := api.RestoreSource{Key: "snapshot", ExpectedClusterID: "source", Target: api.BackupTarget{
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage"},
	}}
	template := &api.RestoreClusterTemplate{Version: "2.7.1", InitContainer: &api.InitContainerConfig{Image: "init"}}
	a := &minimalRestoreFixture{destinationFW: &framework.Framework{Namespace: "destination-a"}, snapshot: snapshot, template: template}
	b := &minimalRestoreFixture{destinationFW: &framework.Framework{Namespace: "destination-b"}, snapshot: snapshot, template: template}
	first := a.newRequest("same-name", api.RestoreTargetLifecycleDisposable)
	second := b.newRequest("same-name", api.RestoreTargetLifecycleDisposable)
	first.Spec.Source.ExpectedClusterID = "wrong-identity"
	first.Spec.Source.Target.CredentialsSecretRef.Name = "reader"
	first.Spec.ClusterTemplate.InitContainer.Image = "changed"
	require.Equal(t, "source", second.Spec.Source.ExpectedClusterID)
	require.Equal(t, "storage", second.Spec.Source.Target.CredentialsSecretRef.Name)
	require.Equal(t, "init", second.Spec.ClusterTemplate.InitContainer.Image)
	require.Equal(t, "source", a.snapshot.ExpectedClusterID)
	require.Equal(t, "storage", snapshot.Target.CredentialsSecretRef.Name)
	require.Equal(t, "init", template.InitContainer.Image)
	require.NotEqual(t, first.Namespace, second.Namespace)
	require.Equal(t, first.Name, second.Name, "namespace ownership prevents cross-group name collisions")
}

func TestE2EFixtureRestoreApprovalAndStorageAreNamespaceOwned(t *testing.T) {
	RegisterTestingT(t)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, networkingv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "destination-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "destination-b"}},
	).Build()
	for _, namespace := range []string{"destination-a", "destination-b"} {
		setMinimalDestinationApproval(t.Context(), c, namespace, true)
	}
	setMinimalDestinationApproval(t.Context(), c, "destination-a", false)
	other := &corev1.Namespace{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: "destination-b"}, other))
	require.Equal(t, "true", other.Labels["openbao.org/restore-target-approved"])
	for _, pair := range [][2]string{{"destination-a", "source-a"}, {"destination-b", "source-b"}} {
		prepareMinimalDestinationNetwork(t.Context(), c, pair[0], pair[1], pair[1]+"-storage")
		policy := &networkingv1.NetworkPolicy{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pair[0], Name: "prepared-executors"}, policy))
		require.Equal(t, pair[1]+"-storage", policy.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
		storage := minimalRestoreStorageConfig(pair[1])
		require.Equal(t, pair[1], storage.Namespace)
		require.Contains(t, minimalRestoreStorageEndpoint(pair[1]), "."+pair[1]+".svc:")
	}
	require.NotEqual(t, minimalRestoreStorageEndpoint("source-a"), minimalRestoreStorageEndpoint("source-b"))
}

func TestE2EFixtureRestoreAPIServerEgressIsScopedToCiliumExecutors(t *testing.T) {
	for _, profile := range []string{"", "ipv4", "ipv6", "dual-ipv4", "dual-ipv6"} {
		t.Run("profile="+profile, func(t *testing.T) {
			RegisterTestingT(t)
			t.Setenv("E2E_NETWORK_PROFILE", profile)
			scheme := runtime.NewScheme()
			require.NoError(t, networkingv1.AddToScheme(scheme))
			gvk := schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("CiliumNetworkPolicyList"), &unstructured.UnstructuredList{})
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			prepareMinimalDestinationNetwork(t.Context(), c, "destination", "source", "storage")

			policies := &unstructured.UnstructuredList{}
			policies.SetGroupVersionKind(gvk.GroupVersion().WithKind("CiliumNetworkPolicyList"))
			require.NoError(t, c.List(t.Context(), policies))
			if profile == "" {
				require.Empty(t, policies.Items, "ordinary Kind runs must not require the Cilium API")
				return
			}
			require.Len(t, policies.Items, 1)
			policy := policies.Items[0]
			require.Equal(t, "destination", policy.GetNamespace())
			require.Empty(t, policy.GetOwnerReferences(), "destination isolation remains administrator-managed")
			require.Equal(t, map[string]interface{}{
				"endpointSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{constants.LabelOpenBaoComponent: "restore"},
				},
				"enableDefaultDeny": map[string]interface{}{"ingress": false, "egress": false},
				"egress": []interface{}{map[string]interface{}{
					"toEntities": []interface{}{"kube-apiserver"},
					"toPorts": []interface{}{map[string]interface{}{
						"ports": []interface{}{map[string]interface{}{"port": "6443", "protocol": "TCP"}},
					}},
				}},
			}, policy.Object["spec"])
		})
	}
}

func TestE2EFixtureUpgradeStorageIsNamespaceOwned(t *testing.T) {
	first := upgradeStorageConfig("upgrade-storage-a")
	second := upgradeStorageConfig("upgrade-storage-b")
	require.NotEqual(t, first.Namespace, second.Namespace)
	require.NotEqual(t, upgradeStorageEndpoint(first.Namespace), upgradeStorageEndpoint(second.Namespace))
	require.Contains(t, upgradeStorageEndpoint(first.Namespace), "."+first.Namespace+".svc:")
	require.Equal(t, first.Name, second.Name, "names are scoped to the owning namespace")
	first.Buckets[0] = "other-bucket"
	require.Equal(t, []string{rustfsBucket}, second.Buckets)
}

func TestE2EFixtureUpgradeStorageCleanupPinsNamespaceUID(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	for _, test := range []struct {
		name string
		uid  types.UID
	}{
		{name: "owned", uid: "fixture-uid"},
		{name: "missing ownership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "upgrade-storage-a", UID: test.uid}}
			other := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "upgrade-storage-b", UID: "other-uid"}}
			deleted := false
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, other).WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleted = true
					options := &client.DeleteOptions{}
					options.ApplyOptions(opts)
					require.Equal(t, ns.Name, obj.GetName())
					require.NotNil(t, options.Preconditions)
					require.Equal(t, test.uid, *options.Preconditions.UID)
					return fmt.Errorf("injected deletion failure")
				},
			}).Build()
			err := cleanupUpgradeStorage(t.Context(), c, ns)
			require.Error(t, err, "cleanup failures must reach the preservation path")
			require.Equal(t, test.uid != "", deleted)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(other), &corev1.Namespace{}))
		})
	}
}
