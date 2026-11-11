//go:build integration

package primitives

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	clusterapp "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/statusapply"
)

// The fake client does not reproduce managedFields or SSA conflicts. This test
// uses the real destination schema and normal status writers against envtest.
func TestImportStatusOwnershipRealAPI(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	require.NotEmpty(t, assets, "KUBEBUILDER_ASSETS must identify installed envtest binaries")
	environment := &envtest.Environment{BinaryAssetsDirectory: assets,
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true}
	for _, kind := range []string{"openbaoclusters", "openbaotenants", "openbaorestores"} {
		encoded, readErr := os.ReadFile(filepath.Join(environment.CRDDirectoryPaths[0], "kubebao.org_"+kind+".yaml"))
		require.NoError(t, readErr)
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, yaml.Unmarshal(encoded, crd))
		crd.Name = kind + ".openbao.org"
		crd.Spec.Group = "openbao.org"
		environment.CRDs = append(environment.CRDs, crd)
	}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	c, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for _, failure := range []string{
		"none", "lost-status-response", "before-release", "lost-release-response", "process-exit",
	} {
		t.Run(failure, func(t *testing.T) {
			request, payload, destination := statusOwnershipAPIFixture(t, c, failure)
			initialManagers := destination.GetManagedFields()
			wrapped := interceptor.NewClient(c, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object,
					patch client.Patch, opts ...client.SubResourcePatchOption,
				) error {
					if err := c.SubResource(name).Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					if failure == "lost-status-response" {
						return errors.New("lost status response")
					}
					return nil
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption,
				) error {
					if failure == "before-release" {
						return errors.New("interrupted before ownership release")
					}
					if err := c.Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					if failure == "lost-release-response" {
						return errors.New("lost ownership release response")
					}
					return nil
				},
			})
			var result ImportResult
			var err error
			if failure == "process-exit" {
				runImportStatusExit(t, config, request)
				err = errors.New("subprocess exited after accepted status and before ownership release")
			} else {
				result, err = Import(t.Context(), wrapped, request)
			}
			if failure == "none" {
				require.NoError(t, err)
				require.True(t, result.StatusImported)
			} else {
				require.Error(t, err)
				require.False(t, result.StatusImported)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), destination))
			fresh, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
			require.NoError(t, err)
			_, pending, err := importStatusOwnership(destination)
			require.NoError(t, err)
			expectPending := failure == "lost-status-response" || failure == "before-release" || failure == "process-exit"
			require.Equal(t, expectPending, pending)
			require.Equal(t, pending, ImportStatusPending(destination))
			if pending {
				cluster := &api.OpenBaoCluster{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), cluster))
				cluster.Status.ReadyReplicas = 3
				err := clusterapp.PatchStatusOwnedFields(t.Context(), c, cluster)
				require.True(t, apierrors.IsConflict(err), "reproduce the original SSA conflict: %v", err)
			}
			// Inspection and a new invocation finish only the missing release. A lost
			// release response needs no second write, and accepted status is never seeded again.
			statusWrites, releaseWrites := 0, 0
			resume := interceptor.NewClient(fresh, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object,
					client.Patch, ...client.SubResourcePatchOption,
				) error {
					statusWrites++
					return errors.New("must not rewrite accepted status")
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption,
				) error {
					releaseWrites++
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
			result, err = Import(t.Context(), resume, request)
			require.NoError(t, err)
			require.True(t, result.StatusImported)
			require.Zero(t, statusWrites)
			if pending {
				require.Equal(t, 1, releaseWrites)
			} else {
				require.Zero(t, releaseWrites)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), destination))
			require.Equal(t, payload.Status, destination.Object["status"])
			require.Equal(t, initialManagers, destination.GetManagedFields(), "preserve unrelated field ownership exactly")
			assertNormalStatusWritersAfterImport(t, c, destination)
		})
	}
	for _, change := range []string{discoveryTestReplacement, "revision"} {
		t.Run("release-rejects-"+change, func(t *testing.T) {
			request, _, destination := statusOwnershipAPIFixture(t, c, "concurrent-"+change)
			wrapped := interceptor.NewClient(c, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption,
				) error {
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(destination), destination))
					if change == discoveryTestReplacement {
						require.NoError(t, c.Delete(ctx, destination, client.Preconditions{UID: &request.ExpectedDestinationUID}))
						destination.SetUID("")
						destination.SetResourceVersion("")
						destination.SetManagedFields(nil)
						delete(destination.Object, "status")
						require.NoError(t, c.Create(ctx, destination))
					} else {
						annotations := destination.GetAnnotations()
						annotations["test.kubebao.org/concurrent"] = "changed"
						destination.SetAnnotations(annotations)
						require.NoError(t, c.Update(ctx, destination))
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
			result, err := Import(t.Context(), wrapped, request)
			require.ErrorContains(t, err, "status ownership release requires inspection")
			require.False(t, result.StatusImported)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), destination))
			if change == discoveryTestReplacement {
				require.NotEqual(t, request.ExpectedDestinationUID, destination.GetUID())
				require.Empty(t, destination.Object["status"])
			} else {
				_, pending, err := importStatusOwnership(destination)
				require.NoError(t, err)
				require.True(t, pending, "stale revision must not release ownership")
			}
		})
	}
}

func statusOwnershipAPIFixture(t *testing.T, c client.Client, name string) (ImportRequest,
	*ImportPayload, *unstructured.Unstructured,
) {
	t.Helper()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "status-import-"}}
	require.NoError(t, c.Create(t.Context(), namespace))
	source, request := sourceFixture(t)
	payload, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err)
	source.SetUID("")
	source.SetResourceVersion("")
	source.SetGeneration(0)
	source.SetNamespace(namespace.Name)
	source.SetName(name)
	require.NoError(t, unstructured.SetNestedMap(source.Object, map[string]any{"size": "1Gi"}, "spec", "storage"))
	require.NoError(t, unstructured.SetNestedMap(source.Object,
		map[string]any{"enabled": true, "mode": "OperatorManaged", "rotationPeriod": "720h"}, "spec", "tls"))
	require.NoError(t, c.Create(t.Context(), source, client.FieldOwner("fixture-spec-manager")))
	seed := payload.Status
	seed["observedGeneration"] = source.GetGeneration()
	seed["conditions"] = []any{map[string]any{"type": "Available", "status": "True", "reason": "Ready",
		"message": "Test source ready", "observedGeneration": source.GetGeneration(),
		"lastTransitionTime": metav1.Now().Format("2006-01-02T15:04:05Z")}}
	seed["workload"] = map[string]any{"lastError": map[string]any{"reason": "SeededObservation"}}
	source.Object["status"] = seed
	require.NoError(t, c.Status().Update(t.Context(), source))
	request.Source = identity(PlanSourceAPIVersion, discoveryClusterKind, source)
	request.NamespaceUID = namespace.UID
	payload, err = BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	request.ExpectedImportDigest = payload.Digest
	request.CreateOnly = true
	created, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	request.ExpectedDestinationUID = created.Destination.UID
	request.CreateOnly = false
	destination := payload.Object.DeepCopy()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), destination))
	return request, payload, destination
}

func assertNormalStatusWritersAfterImport(t *testing.T, c client.Client, destination *unstructured.Unstructured) {
	t.Helper()
	cluster := &api.OpenBaoCluster{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), cluster))
	cluster.Status.ReadyReplicas = 3
	cluster.Status.CurrentVersion = "2.6.5"
	cluster.Status.ObservedGeneration = cluster.Generation
	require.NoError(t, clusterapp.PatchStatusOwnedFields(t.Context(), c, cluster))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), cluster))
	original := cluster.DeepCopy()
	cluster.Status.Workload.LastError = &api.ControllerErrorStatus{Reason: "UpdatedObservation"}
	require.NoError(t, clusterapp.PatchWorkloadOwnedFields(t.Context(), c, logr.Discard(), original, cluster, "test"))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), cluster))
	cluster.Status.AcceptedUpgradeStrategy = api.UpdateStrategyBlueGreen
	require.NoError(t, statusapply.ApplyOpenBaoClusterAdminOpsStatus(t.Context(), c, cluster,
		statusapply.OpenBaoClusterAdminOpsStatusApplyOptions{}))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), cluster))
	require.EqualValues(t, 3, cluster.Status.ReadyReplicas)
	require.Equal(t, "2.6.5", cluster.Status.CurrentVersion)
	require.Equal(t, cluster.Generation, cluster.Status.ObservedGeneration)
	require.Equal(t, "UpdatedObservation", cluster.Status.Workload.LastError.Reason)
	require.Equal(t, api.UpdateStrategyBlueGreen, cluster.Status.AcceptedUpgradeStrategy)
}
