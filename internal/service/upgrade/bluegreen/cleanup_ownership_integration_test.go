//go:build integration

package bluegreen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// Keep these API-server tests next to the private cleanup methods: no phase or
// Raft stubs are needed to verify real UID and resourceVersion delete semantics.
func TestStatefulSetCleanupAPIServerPreconditions(t *testing.T) {
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	c, err := client.NewWithWatch(cfg, client.Options{Scheme: newBlueGreenTestScheme(t)})
	require.NoError(t, err)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cleanup-ownership"}}
	require.NoError(t, c.Create(t.Context(), namespace))

	for _, blue := range []bool{false, true} {
		for _, mode := range []string{"owned", "foreign", "updated", "replaced"} {
			name := "abort-" + mode
			if blue {
				name = "forward-" + mode
			}
			t.Run(name, func(t *testing.T) {
				cluster := newPhaseMachineCluster()
				cluster.Name, cluster.Namespace, cluster.UID = name, namespace.Name, ""
				cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
				cluster.Spec.Upgrade.JWTAuthRole = "upgrade"
				require.NoError(t, c.Create(t.Context(), cluster))
				cluster.Status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{
					Phase: openbaov1alpha1.PhaseCleanup, BlueRevision: "blue", GreenRevision: "green",
				}
				revision := cluster.Status.BlueGreen.GreenRevision
				if blue {
					revision = cluster.Status.BlueGreen.BlueRevision
				}
				sts := newManagedCleanupStatefulSet(cluster, revision)
				sts.UID = ""
				if mode == "foreign" {
					sts.OwnerReferences[0].UID = "another-cluster-uid"
				}
				if mode == "owned" {
					sts.Finalizers = []string{"test.openbao.org/hold-deletion"}
				}
				require.NoError(t, c.Create(t.Context(), sts))
				observedUID := sts.UID
				var replacement *appsv1.StatefulSet
				deletes := 0
				writer := interceptor.NewClient(c, interceptor.Funcs{
					Delete: func(ctx context.Context, base client.WithWatch, object client.Object,
						opts ...client.DeleteOption,
					) error {
						deletes++
						options := (&client.DeleteOptions{}).ApplyOptions(opts)
						require.NotNil(t, options.Preconditions)
						require.NotNil(t, options.Preconditions.UID)
						require.NotNil(t, options.Preconditions.ResourceVersion)
						require.Equal(t, object.GetUID(), *options.Preconditions.UID)
						require.Equal(t, object.GetResourceVersion(), *options.Preconditions.ResourceVersion)
						switch mode {
						case "updated":
							changed := &appsv1.StatefulSet{}
							require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(object), changed))
							changed.Annotations["test.openbao.org/changed"] = "after-read"
							require.NoError(t, base.Update(ctx, changed))
						case "replaced":
							require.NoError(t, base.Delete(ctx, object, client.PropagationPolicy(metav1.DeletePropagationBackground)))
							replacement = newManagedCleanupStatefulSet(cluster, revision)
							replacement.UID, replacement.OwnerReferences, replacement.Annotations = "", nil, nil
							require.NoError(t, base.Create(ctx, replacement))
						}
						return base.Delete(ctx, object, opts...)
					},
				})
				manager := &Manager{client: writer, reader: c}
				cleanupErr := runStatefulSetCleanup(t, manager, cluster, blue)
				current := &appsv1.StatefulSet{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), current))
				switch mode {
				case "owned":
					require.NoError(t, cleanupErr)
					require.NotNil(t, current.DeletionTimestamp)
					require.NoError(t, runStatefulSetCleanup(t, manager, cluster, blue))
					require.Equal(t, 1, deletes, "lost status must not repeat an acknowledged deletion")
					current.Finalizers = nil
					require.NoError(t, c.Update(t.Context(), current))
					require.NoError(t, runStatefulSetCleanup(t, manager, cluster, blue))
				case "foreign":
					require.ErrorContains(t, cleanupErr, "managed controller owner")
					require.Equal(t, observedUID, current.UID)
					require.Nil(t, current.DeletionTimestamp)
					require.Zero(t, deletes)
				case "updated":
					require.True(t, apierrors.IsConflict(cleanupErr), "error: %v", cleanupErr)
					require.Equal(t, observedUID, current.UID)
					require.Nil(t, current.DeletionTimestamp)
				case "replaced":
					require.True(t, apierrors.IsConflict(cleanupErr), "error: %v", cleanupErr)
					require.Equal(t, replacement.UID, current.UID)
					require.NotEqual(t, observedUID, current.UID)
					require.Nil(t, current.DeletionTimestamp)
					require.ErrorContains(t, runStatefulSetCleanup(t, manager, cluster, blue), "managed controller owner")
					require.Equal(t, 1, deletes, "fresh observation must reject the foreign replacement")
				}
			})
		}
	}
}
