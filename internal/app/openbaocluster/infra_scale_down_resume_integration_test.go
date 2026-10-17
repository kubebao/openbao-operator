//go:build integration

package openbaocluster

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// These private recovery fixtures keep API-server UID/RV preconditions next to
// the replica-only patch; no test-only production export is needed.
func TestScaleDownResumeRealAPIPreconditions(t *testing.T) {
	testEnv := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	live, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for i, tc := range []struct {
		name        string
		readReplica bool
		race        string
	}{
		{name: "voter recovery"},
		{name: "read-replica recovery", readReplica: true},
		{name: "concurrent workload change", race: "update"},
		{name: "same-name workload replacement", race: "replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readers := int32(-1)
			if tc.readReplica {
				readers = 1
			}
			f := newScaleDownResumeFixture(t, tc.readReplica, readers)
			ns := fmt.Sprintf("scale-down-resume-%d", i)
			require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
			createScaleDownResumeAPIFixture(t, live, f, ns)
			var writer client.Client = live
			if tc.race != "" {
				writer = interceptor.NewClient(live, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					current := &appsv1.StatefulSet{}
					require.NoError(t, live.Get(ctx, client.ObjectKeyFromObject(f.sts), current))
					if tc.race == "update" {
						current.Annotations["test.openbao.org/concurrent"] = "retained"
						require.NoError(t, live.Update(ctx, current))
					} else {
						require.NoError(t, live.Delete(ctx, current))
						current.UID = ""
						current.ResourceVersion = ""
						current.Generation = 0
						current.Status = appsv1.StatefulSetStatus{}
						require.NoError(t, live.Create(ctx, current))
					}
					return c.Patch(ctx, obj, patch, opts...)
				}})
			}
			// A fresh reconciler has no in-memory receipt of the preceding peer
			// removal. Recovery depends on current authenticated observations.
			r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: writer, APIReader: live}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
			if tc.race == "" {
				require.True(t, f.cluster.Status.Initialized)
				require.False(t, statefulSetSettledAtReplicas(f.sts, *f.sts.Spec.Replicas))
				result, reconcileErr := r.Reconcile(t.Context(), logr.Discard(), f.cluster)
				require.NoError(t, reconcileErr)
				require.Positive(t, result.RequeueAfter)
			} else {
				resumed, resumeErr := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, tc.readReplica)
				require.False(t, resumed)
				require.Error(t, resumeErr)
				require.True(t, apierrors.IsConflict(resumeErr) || apierrors.IsInvalid(resumeErr), "%v", resumeErr)
			}
			stored := &appsv1.StatefulSet{}
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(f.sts), stored))
			want := *f.sts.Spec.Replicas
			if tc.race == "" {
				want--
			}
			require.Equal(t, want, *stored.Spec.Replicas)
			require.Equal(t, f.sts.Spec.Template, stored.Spec.Template)
		})
	}
}

func TestScaleDownResumeClusterDecisionRealAPI(t *testing.T) {
	testEnv := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	live, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	index := 0
	for _, readReplica := range []bool{false, true} {
		for _, during := range []bool{false, true} {
			for _, change := range scaleDownResumeClusterChanges() {
				switch change.name {
				case "status observations", "lock renewal", "spec retarget", "active voter revision", "restore hold", "reader drain cancelled", "rolling execution ID":
				default:
					continue
				}
				name := fmt.Sprintf("%s/during=%t/reader=%t", change.name, during, readReplica)
				t.Run(name, func(t *testing.T) {
					readers := int32(-1)
					if readReplica {
						readers = 1
					}
					f := newScaleDownResumeFixture(t, readReplica, readers)
					if change.setup != nil {
						change.setup(f)
					}
					ns := fmt.Sprintf("scale-decision-%d", index)
					index++
					require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
					createScaleDownResumeAPIFixture(t, live, f, ns)
					update := func() {
						current := &openbaov1alpha1.OpenBaoCluster{}
						require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(f.cluster), current))
						change.mutate(current)
						if change.metadata {
							require.NoError(t, live.Update(t.Context(), current))
						} else {
							require.NoError(t, live.Status().Update(t.Context(), current))
						}
						require.NotEqual(t, f.cluster.ResourceVersion, current.ResourceVersion)
						if change.name == "spec retarget" {
							require.Greater(t, current.Generation, f.cluster.Generation)
						} else {
							require.Equal(t, f.cluster.Generation, current.Generation)
						}
					}
					if during {
						f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
							if call == 2 {
								update()
							}
							return f.membership.config, nil
						}
					} else {
						update()
					}
					r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: live, APIReader: live}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
					resumed, resumeErr := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
					if change.allowed {
						require.NoError(t, resumeErr)
						require.True(t, resumed)
					} else {
						require.Error(t, resumeErr)
						require.False(t, resumed)
					}
					stored := &appsv1.StatefulSet{}
					require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(f.sts), stored))
					want := *f.sts.Spec.Replicas
					if change.allowed {
						want--
					}
					require.Equal(t, want, *stored.Spec.Replicas)
					require.Equal(t, f.sts.Spec.Template, stored.Spec.Template)
				})
			}
		}
	}
}

func createScaleDownResumeAPIFixture(t *testing.T, c client.Client, f *scaleDownResumeFixture, namespace string) {
	t.Helper()
	cluster := f.cluster
	cluster.Namespace = namespace
	cluster.UID = ""
	cluster.ResourceVersion = ""
	cluster.Spec.Version = "2.6.4"
	cluster.Spec.Image = "openbao/openbao:2.6.4"
	cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
	cluster.Spec.TLS = openbaov1alpha1.TLSConfig{Enabled: true, RotationPeriod: "720h"}
	cluster.Spec.Storage = openbaov1alpha1.StorageConfig{Size: "1Gi"}
	cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{Image: "openbao/openbao-init:test"}
	status := cluster.Status
	require.NoError(t, c.Create(t.Context(), cluster))
	cluster.Status = status
	cluster.Status.Initialized = true
	require.NoError(t, c.Status().Update(t.Context(), cluster))
	workloads := map[string]*appsv1.StatefulSet{}
	for _, obj := range f.objects {
		sts, ok := obj.(*appsv1.StatefulSet)
		if !ok {
			continue
		}
		status := sts.Status
		sts.Namespace = namespace
		sts.UID = ""
		sts.ResourceVersion = ""
		sts.Generation = 0
		sts.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}
		sts.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(cluster.UID)
		labels := map[string]string{"app": sts.Name}
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		sts.Spec.Template.Labels = labels
		require.NoError(t, c.Create(t.Context(), sts))
		sts.Status = status
		sts.Status.ObservedGeneration = sts.Generation
		require.NoError(t, c.Status().Update(t.Context(), sts))
		workloads[sts.Name] = sts
	}
	for _, obj := range f.objects {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			continue
		}
		status := pod.Status
		pod.Namespace = namespace
		sts := workloads[pod.OwnerReferences[0].Name]
		pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}
		pod.Spec.Containers = []corev1.Container{{Name: constants.ContainerBao, Image: "openbao/openbao:old"}}
		require.NoError(t, c.Create(t.Context(), pod))
		pod.Status = status
		require.NoError(t, c.Status().Update(t.Context(), pod))
	}
	for i := range f.membership.config.Config.Servers {
		server := &f.membership.config.Config.Servers[i]
		server.Address = strings.ReplaceAll(server.Address, ".default.svc", "."+namespace+".svc")
		health := f.membership.health.Servers[server.NodeID]
		health.Address = server.Address
		f.membership.health.Servers[server.NodeID] = health
	}
}
