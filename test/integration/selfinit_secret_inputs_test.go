//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func secretInputCluster(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	cluster := newMinimalClusterObj(namespace, name)
	cluster.Spec.Version = "2.6.0"
	cluster.Spec.Image = "openbao/openbao:2.6.0"
	cluster.Spec.InitContainer = nil
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{{
		Name: "login", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "auth/userpass/users/admin",
		DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{
			"password": {SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap-login", Key: "password"}},
		},
	}}}
	return cluster
}

func TestSelfInitSecretInputAdmission(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	const username = "bootstrap-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	editor := newImpersonatedClient(t, username)
	cluster := secretInputCluster(namespace, "unauthorized")
	err := editor.Create(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "authorized to get every referenced Secret")

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-reader", Namespace: namespace}, Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"bootstrap-login"}, Verbs: []string{"get"},
	}}}
	require.NoError(t, k8sClient.Create(ctx, role))
	require.NoError(t, k8sClient.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: username, APIGroup: rbacv1.GroupName}},
	}))
	waitForResourceAuthorization(t, username, namespace, "", "secrets", "bootstrap-login", "get")
	// Admission authorizes the reference without reading an existing Secret.
	cluster.Name = "authorized"
	require.NoError(t, editor.Create(ctx, cluster))
	cluster.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "other-secret", Key: "password"},
	}
	err = editor.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "immutable after cluster creation")

	for index, edit := range []func(*openbaov1alpha1.OpenBaoCluster){
		func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.Version = "2.5.5" },
		func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.SelfInit.Requests[0].Path = "/sys/auth/human" },
		func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{}
		},
		func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{
				SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bao-root-token", Key: "token"},
			}
		},
	} {
		candidate := secretInputCluster(namespace, fmt.Sprintf("invalid-%d", index))
		edit(candidate)
		require.Error(t, k8sClient.Create(ctx, candidate, client.DryRunAll))
	}
}

func TestSelfInitSecretInputsRemovedAfterInitialization(t *testing.T) {
	for name, object := range map[string]client.Object{
		"openbao-lock-controller-statefulset-mutations.yaml":         &admissionv1.ValidatingAdmissionPolicy{},
		"openbao-lock-controller-statefulset-mutations-binding.yaml": &admissionv1.ValidatingAdmissionPolicyBinding{},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", name))
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(data, object))
		require.NoError(t, k8sClient.Create(ctx, object))
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), object) })
	}
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	cluster := secretInputCluster(namespace, "bootstrap-lifecycle")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	createTLSSecret(t, namespace, cluster.Name)
	controller := newControllerClient(t)
	t.Setenv("OPERATOR_VERSION", "0.0.0")
	spec := newTestStatefulSetSpec(cluster)
	require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), controller, k8sScheme, cluster, spec))
	sts := &appsv1.StatefulSet{}
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	require.NoError(t, k8sClient.Get(ctx, key, sts))
	encoded, err := json.Marshal(sts.Spec.Template)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"bootstrap-login"`)
	require.Eventually(t, func() bool {
		candidate := sts.DeepCopy()
		candidate.Spec.Template.Spec.Volumes[0].ConfigMap.Name = "unrelated-config"
		err := controller.Patch(ctx, candidate, client.MergeFrom(sts), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "cannot modify StatefulSet volumes")
	}, 10*time.Second, 100*time.Millisecond)
	cleanup := sts.DeepCopy()
	cleanup.Spec.Template.Spec.Volumes = slices.DeleteFunc(cleanup.Spec.Template.Spec.Volumes,
		func(v corev1.Volume) bool { return v.Name == "self-init-inputs" })
	removeMount := func(m corev1.VolumeMount) bool { return m.Name == "self-init-inputs" }
	cleanup.Spec.Template.Spec.Containers[0].VolumeMounts = slices.DeleteFunc(
		cleanup.Spec.Template.Spec.Containers[0].VolumeMounts, removeMount)
	init := &cleanup.Spec.Template.Spec.InitContainers[0]
	init.VolumeMounts = slices.DeleteFunc(init.VolumeMounts, removeMount)
	init.Args = slices.DeleteFunc(init.Args, func(a string) bool { return strings.HasPrefix(a, "--self-init-secret-file=") })
	require.NoError(t, controller.Patch(ctx, cleanup, client.MergeFrom(sts), client.DryRunAll))
	for name, mutate := range map[string]func(*appsv1.StatefulSet){
		"redirect another volume": func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.Volumes[0].ConfigMap.Name = "unrelated" },
		"replace the command":     func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.Containers[0].Command = []string{"/other"} },
		"add init arguments": func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.InitContainers[0].Args = append(s.Spec.Template.Spec.InitContainers[0].Args, "--output=/other")
		},
		"change another mount": func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.Containers[0].VolumeMounts[0].ReadOnly = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cleanup.DeepCopy()
			mutate(candidate)
			requireAdmissionDenied(t, controller.Patch(ctx, candidate, client.MergeFrom(sts), client.DryRunAll))
		})
	}

	// No bootstrap Secret exists. Rendering the initialized workload must still
	// succeed and SSA must remove its old projection, mounts, and preflight args.
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized, status.SelfInitialized = true, true
	})
	require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), controller, k8sScheme, cluster, spec))
	require.NoError(t, k8sClient.Get(ctx, key, sts))
	encoded, err = json.Marshal(sts.Spec.Template)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"bootstrap-login"`)
	require.NotContains(t, string(encoded), constants.PathSelfInitInputs)
	require.NotContains(t, string(encoded), "self-init-secret-file")
	require.Empty(t, sts.Spec.Template.Spec.InitContainers[0].EnvFrom)
}

func TestSelfInitSecretInputControllerMetadataUpdates(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	grantTenantOpenBaoWriteAccess(t, namespace, controllerUsername)
	cluster := secretInputCluster(namespace, "controller-metadata")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	controller := newImpersonatedClient(t, controllerUsername)
	cluster.Labels = map[string]string{"bootstrap-test": "metadata-only"}
	require.NoError(t, controller.Update(ctx, cluster, client.DryRunAll))
	cluster.Spec.Replicas++
	err := controller.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "authorized to get every referenced Secret")
}

func TestSelfInitSecretInputUpgradeWithoutSecretAccess(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	const username = "initialized-cluster-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	cluster := secretInputCluster(namespace, "upgrade-without-bootstrap-access")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	editor := newImpersonatedClient(t, username)
	cluster.Spec.Version = "2.6.4"
	// Unchanged references still need authorization before initialization.
	err := editor.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized, status.SelfInitialized = true, true
	})
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	cluster.Spec.Version = "2.6.4"
	// The editor has no Secret read permission, and no bootstrap Secret exists.
	require.NoError(t, editor.Update(ctx, cluster, client.DryRunAll))
	cluster.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "other-secret", Key: "password"},
	}
	requireAdmissionDenied(t, editor.Update(ctx, cluster, client.DryRunAll))
}

func TestSelfInitSecretInputAuthorizationBudget(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	const username = "multiple-input-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "input-readers", Namespace: namespace}, Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"secrets"},
		ResourceNames: []string{"bootstrap-login", "second-source", "third-source"}, Verbs: []string{"get"},
	}}}
	require.NoError(t, k8sClient.Create(ctx, role))
	require.NoError(t, k8sClient.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: username, APIGroup: rbacv1.GroupName}},
	}))
	for _, name := range role.Rules[0].ResourceNames {
		waitForResourceAuthorization(t, username, namespace, "", "secrets", name, "get")
	}
	editor := newImpersonatedClient(t, username)
	cluster := secretInputCluster(namespace, "many-inputs")
	request := &cluster.Spec.SelfInit.Requests[0]
	request.DataFrom = make(map[string]openbaov1alpha1.SelfInitDataSource)
	for i := range 64 {
		request.DataFrom[fmt.Sprintf("field_%d", i)] = openbaov1alpha1.SelfInitDataSource{
			SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap-login", Key: fmt.Sprintf("key-%d", i)},
		}
	}
	// Many keys in one source must charge only one authorization check.
	require.NoError(t, editor.Create(ctx, cluster, client.DryRunAll))
	second := request.DeepCopy()
	second.Name = "second-request"
	cluster.Spec.SelfInit.Requests = append(cluster.Spec.SelfInit.Requests, *second)
	// Deduplication must also span different requests.
	require.NoError(t, editor.Create(ctx, cluster, client.DryRunAll))
	cluster.Spec.SelfInit.Requests[1].DataFrom["field_0"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "second-source", Key: "credential"},
	}
	require.NoError(t, editor.Create(ctx, cluster, client.DryRunAll))
	maximum := cluster.DeepCopy()
	maximum.Spec.SelfInit.Requests = nil
	for i := range 64 {
		request := cluster.Spec.SelfInit.Requests[i%2].DeepCopy()
		request.Name = fmt.Sprintf("request-%d", i)
		maximum.Spec.SelfInit.Requests = append(maximum.Spec.SelfInit.Requests, *request)
	}
	require.NoError(t, editor.Create(ctx, maximum, client.DryRunAll))
	cluster.Spec.SelfInit.Requests[0].DataFrom["field_1"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "unauthorized-source", Key: "credential"},
	}
	delete(cluster.Spec.SelfInit.Requests[1].DataFrom, "field_0")
	err := editor.Create(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "authorized to get every referenced Secret")
	cluster.Spec.SelfInit.Requests[0].DataFrom["field_1"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "third-source", Key: "credential"},
	}
	cluster.Spec.SelfInit.Requests[1].DataFrom["field_0"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "second-source", Key: "credential"},
	}
	err = editor.Create(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "at most two distinct Secrets")
}

func TestSelfInitSecretInputMappingsImmutable(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	cluster := secretInputCluster(namespace, "immutable-inputs")
	second := cluster.Spec.SelfInit.Requests[0].DeepCopy()
	second.Name = "second-request"
	cluster.Spec.SelfInit.Requests = append(cluster.Spec.SelfInit.Requests, *second)
	require.NoError(t, k8sClient.Create(ctx, cluster))
	for name, mutate := range map[string]func(*openbaov1alpha1.OpenBaoCluster){
		"Secret name": func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{
				SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "other-source", Key: "password"},
			}
		},
		"Secret key": func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{
				SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap-login", Key: "other-key"},
			}
		},
		"field name": func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["other_field"] = c.Spec.SelfInit.Requests[0].DataFrom["password"]
			delete(c.Spec.SelfInit.Requests[0].DataFrom, "password")
		},
		"request name": func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.SelfInit.Requests[0].Name = "renamed-request" },
		"added field": func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Spec.SelfInit.Requests[0].DataFrom["extra"] = c.Spec.SelfInit.Requests[0].DataFrom["password"]
		},
		"removed mapping":   func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.SelfInit.Requests[0].DataFrom = nil },
		"removed request":   func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.SelfInit.Requests = c.Spec.SelfInit.Requests[1:] },
		"removed self-init": func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.SelfInit = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cluster.DeepCopy()
			mutate(candidate)
			err := k8sClient.Update(ctx, candidate, client.DryRunAll)
			requireAdmissionDenied(t, err)
			require.ErrorContains(t, err, "immutable after cluster creation")
		})
	}
	// The projection identity does not depend on request order or inline data.
	candidate := cluster.DeepCopy()
	slices.Reverse(candidate.Spec.SelfInit.Requests)
	candidate.Spec.SelfInit.Requests[0].Data = &apiextensionsv1.JSON{Raw: []byte(`{"token_policies":["default"]}`)}
	require.NoError(t, k8sClient.Update(ctx, candidate, client.DryRunAll))
	withoutInputs := secretInputCluster(namespace, "initially-no-inputs")
	withoutInputs.Spec.SelfInit.Requests[0].DataFrom = nil
	require.NoError(t, k8sClient.Create(ctx, withoutInputs))
	withoutInputs.Spec.SelfInit.Requests[0].DataFrom = cluster.Spec.SelfInit.Requests[0].DataFrom
	err := k8sClient.Update(ctx, withoutInputs, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "immutable after cluster creation")
	disabled := secretInputCluster(namespace, "disabled-immutable-inputs")
	disabled.Spec.SelfInit.Requests = append(disabled.Spec.SelfInit.Requests, *second)
	require.NoError(t, k8sClient.Create(ctx, disabled))
	updateClusterStatus(t, disabled, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized, status.SelfInitialized = true, true
	})
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(disabled), disabled))
	disabled.Spec.SelfInit.Enabled = false
	require.NoError(t, k8sClient.Update(ctx, disabled))
	disabled.Spec.SelfInit.Requests[1] = *disabled.Spec.SelfInit.Requests[0].DeepCopy()
	err = k8sClient.Update(ctx, disabled, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "unique source-bearing request names")
}

func TestSelfInitSecretInputEnablement(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	disabled := secretInputCluster(namespace, "disabled-inputs")
	disabled.Spec.SelfInit.Enabled = false
	err := k8sClient.Create(ctx, disabled, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "must be enabled at creation and until initialization completes")
	cluster := secretInputCluster(namespace, "enabled-inputs")
	require.NoError(t, k8sClient.Create(ctx, cluster))
	cluster.Spec.SelfInit.Enabled = false
	err = k8sClient.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "must be enabled at creation and until initialization completes")
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized, status.SelfInitialized = true, true
	})
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	cluster.Spec.SelfInit.Enabled = false
	// Completed bootstrap can be disabled or re-enabled without replaying it.
	require.NoError(t, k8sClient.Update(ctx, cluster))
	cluster.Spec.SelfInit.Enabled = true
	require.NoError(t, k8sClient.Update(ctx, cluster, client.DryRunAll))
}
