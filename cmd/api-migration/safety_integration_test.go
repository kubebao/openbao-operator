//go:build integration

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

// Exercise deletion acceptance, RBAC, and the complete admission policy against
// an API server. Optional network controllers and their data planes are absent.
func TestMigrationSafetyRealAPI(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	require.NotEmpty(t, assets)
	environment := &envtest.Environment{BinaryAssetsDirectory: assets}
	for _, file := range []string{
		"test/manifests/gateway-api/v1.5.1/crds/gateway.networking.k8s.io_httproutes.yaml",
		"test/manifests/gateway-api/v1.5.1/crds/gateway.networking.k8s.io_tlsroutes.yaml",
		"test/manifests/gateway-api/v1.5.1/crds/gateway.networking.k8s.io_backendtlspolicies.yaml",
		"test/manifests/prometheus-operator/v0.91.0/crds/monitoring.coreos.com_servicemonitors.yaml",
		"test/fixtures/cilium/ciliumnetworkpolicies.yaml",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", file))
		require.NoError(t, err)
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, yaml.Unmarshal(data, crd))
		if crd.Spec.Group == "cilium.io" {
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["specs"] = apiextensionsv1.JSONSchemaProps{
				Type: "array", Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: ptr.To(true)}},
			}
		}
		environment.CRDs = append(environment.CRDs, crd)
	}
	// Only root identity is relevant to these custody tests. Import-schema
	// compatibility has separate tests with the released CRDs.
	for _, group := range []string{"openbao.org", "kubebao.org"} {
		for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
			plural := strings.ToLower(kind) + "s"
			environment.CRDs = append(environment.CRDs, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: group, Scope: apiextensionsv1.NamespaceScoped,
					Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Kind: kind},
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object", XPreserveUnknownFields: ptr.To(true)}}}}}})
		}
	}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))
	require.NoError(t, admissionv1.AddToScheme(scheme))
	c, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}
	require.NoError(t, c.Create(t.Context(), namespace))

	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		t.Run("accepted-delete-"+kind, func(t *testing.T) {
			root := apiTestObject(testSourceAPI, kind, namespace.Name, "retiring")
			require.NoError(t, c.Create(t.Context(), root))
			require.NoError(t, c.Delete(t.Context(), root, client.PropagationPolicy(metav1.DeletePropagationOrphan)))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(root), root))
			require.NotNil(t, root.GetDeletionTimestamp())
			p := &Procedure{kube: &Kube{client: c}, log: io.Discard, plan: &Plan{Phase: phaseCommitting,
				Progress: Progress{Command: "commit", Completed: []string{"verify"}}}}
			if kind == clusterKind {
				p.plan.Clusters = []ClusterPlan{{Namespace: namespace.Name, Name: root.GetName(), SourceUID: root.GetUID()}}
			} else {
				record := RecordPlan{Kind: kind, Namespace: namespace.Name, Name: root.GetName(), SourceUID: root.GetUID()}
				if kind == restoreKind {
					p.plan.Restores = []RecordPlan{record}
				} else {
					p.plan.Tenants = []RecordPlan{record}
				}
			}
			require.NoError(t, p.preflightSourceRetirement(t.Context()), "commit must accept its pending orphan deletion")
			require.ErrorContains(t, p.Rollback(t.Context()), "being deleted")
			require.Equal(t, phaseCommitting, p.plan.Phase)
			require.Equal(t, "commit", p.plan.Progress.Command)
		})
	}
	t.Run("finalizer-added-before-delete", func(t *testing.T) {
		root := apiTestObject(testSourceAPI, clusterKind, namespace.Name, "racing")
		root.SetFinalizers([]string{clusterFinalizer})
		require.NoError(t, c.Create(t.Context(), root))
		wrapped := interceptor.NewClient(c, interceptor.Funcs{
			Delete: func(ctx context.Context, base client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
				fresh := root.DeepCopy()
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(root), fresh))
				fresh.SetFinalizers([]string{"example.org/protection"})
				require.NoError(t, base.Update(ctx, fresh))
				return base.Delete(ctx, object, opts...)
			},
		})
		err := (&Kube{client: wrapped}).removeFinalizerAndDelete(t.Context(), testSourceAPI, clusterKind,
			namespace.Name, root.GetName(), root.GetUID())
		require.True(t, apierrors.IsConflict(err), "revision must reject a concurrent finalizer: %v", err)
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(root), root))
		require.Nil(t, root.GetDeletionTimestamp())
		require.Equal(t, []string{"example.org/protection"}, root.GetFinalizers())
	})

	source := apiTestObject(testSourceAPI, clusterKind, namespace.Name, testClusterName)
	destination := apiTestObject(testDestAPI, clusterKind, namespace.Name, testClusterName)
	require.NoError(t, c.Create(t.Context(), source))
	require.NoError(t, c.Create(t.Context(), destination))
	resources := make([]*unstructured.Unstructured, 0, len(networkKinds))
	for _, kind := range networkKinds {
		object := networkAPIFixture(kind)
		meta := managedMeta(testSourceAPI, source.GetUID())
		object.SetName(strings.ToLower(kind.Kind))
		object.SetNamespace(namespace.Name)
		object.SetLabels(meta.Labels)
		object.SetAnnotations(meta.Annotations)
		object.SetOwnerReferences(meta.OwnerReferences)
		require.NoError(t, c.Create(t.Context(), object))
		if kind.Kind == networkCiliumKind {
			object.Object["status"] = map[string]any{"nodes": map[string]any{"fixture": map[string]any{"ok": true}}}
			require.NoError(t, c.Status().Update(t.Context(), object))
		}
		resources = append(resources, object)
	}
	account := apiTestObject("v1", "ServiceAccount", namespace.Name, testClusterName+"-upgrade-serviceaccount")
	meta := managedMeta("", source.GetUID())
	meta.Labels["openbao.org/service-account-role"] = "upgrade"
	account.SetLabels(meta.Labels)
	account.SetAnnotations(meta.Annotations)
	account.Object["automountServiceAccountToken"] = false
	account.Object["imagePullSecrets"] = []any{map[string]any{"name": "private-images"}}
	require.NoError(t, c.Create(t.Context(), account))
	resources = append(resources, account)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "migrator", Namespace: namespace.Name}, Rules: migratorRules()}
	require.NoError(t, c.Create(t.Context(), role))
	require.NoError(t, c.Create(t.Context(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace.Name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "openbao", Name: "migrator"}},
	}))
	policy := openedLockPolicy(t)
	require.NoError(t, c.Create(t.Context(), &policy))
	require.NoError(t, c.Create(t.Context(), &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: policy.Name}, Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName: policy.Name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}},
	}))
	writeConfig := rest.CopyConfig(config)
	writeConfig.Impersonate.UserName = testMigrator
	writer, err := client.New(writeConfig, client.Options{Scheme: scheme})
	require.NoError(t, err)
	handoff := &handoffClient{Client: c, writer: writer}
	for _, object := range resources {
		t.Run("handoff-"+object.GetKind(), func(t *testing.T) {
			before := object.DeepCopy()
			request := primitives.TransferRequest{NamespaceUID: namespace.UID,
				Source: apiTestIdentity(source), Destination: apiTestIdentity(destination), Resource: apiTestIdentity(object)}
			request.Retained = object.GetKind() == "ServiceAccount"
			// Wait for admission and RBAC caches. A mutation outside the
			// metadata-only handoff must stay denied.
			invalid := []byte(`[{"op":"add","path":"/metadata/annotations/openbao.org~1owner-uid","value":"forged"}]`)
			if request.Retained {
				invalid = []byte(`[{"op":"add","path":"/automountServiceAccountToken","value":true}]`)
			}
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				denied := writer.Patch(t.Context(), object.DeepCopy(), client.RawPatch("application/json-patch+json", invalid), client.DryRunAll)
				assert.ErrorContains(collect, denied, policy.Name)
			}, 10*time.Second, 100*time.Millisecond)
			require.NoError(t, primitives.TransferOwnership(t.Context(), handoff, request))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(object), object))
			if request.Retained {
				require.Empty(t, object.GetOwnerReferences())
				require.Equal(t, string(destination.GetUID()), object.GetAnnotations()[testOwnerUIDKey])
			} else {
				require.Equal(t, destination.GetUID(), object.GetOwnerReferences()[0].UID)
			}
			request.Source, request.Destination = request.Destination, request.Source
			require.NoError(t, primitives.TransferOwnership(t.Context(), handoff, request))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(object), object))
			require.Equal(t, before.GetUID(), object.GetUID())
			require.Equal(t, before.GetOwnerReferences(), object.GetOwnerReferences())
			for _, field := range []string{"spec", "specs", "status", "secrets", "imagePullSecrets", "automountServiceAccountToken"} {
				require.Equal(t, before.Object[field], object.Object[field], "%s must be preserved", field)
			}
		})
	}
}

func apiTestIdentity(object *unstructured.Unstructured) primitives.TransferIdentity {
	return primitives.TransferIdentity{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(),
		Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func networkAPIFixture(kind schema.GroupVersionKind) *unstructured.Unstructured {
	backend := map[string]any{"name": "bao", "port": int64(8200)}
	spec := map[string]any{}
	switch kind.Kind {
	case "Ingress":
		spec["defaultBackend"] = map[string]any{"service": map[string]any{
			"name": "bao", "port": map[string]any{"number": int64(8200)}}}
	case "HTTPRoute", "TLSRoute":
		spec["rules"] = []any{map[string]any{"backendRefs": []any{backend}}}
		if kind.Kind == "TLSRoute" {
			spec["hostnames"] = []any{"bao.example"}
		}
	case "BackendTLSPolicy":
		spec["targetRefs"] = []any{map[string]any{"group": "", "kind": "Service", "name": "bao"}}
		spec["validation"] = map[string]any{"hostname": "bao.example", "wellKnownCACertificates": "System"}
	case "ServiceMonitor":
		spec["selector"] = map[string]any{"matchLabels": map[string]any{"app": "bao"}}
		spec["endpoints"] = []any{map[string]any{"port": "metrics"}}
	case networkCiliumKind:
		spec["endpointSelector"] = map[string]any{"matchLabels": map[string]any{"app": "bao"}}
	}
	object := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	object.SetGroupVersionKind(kind)
	if kind.Kind == networkCiliumKind {
		object.Object["specs"] = []any{map[string]any{"endpointSelector": map[string]any{}}}
	}
	return object
}

func apiTestObject(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{}}
	object.SetAPIVersion(apiVersion)
	object.SetKind(kind)
	object.SetNamespace(namespace)
	object.SetName(name)
	return object
}
