//go:build integration

package openbaocluster

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestScaleDownResumeTerminatingVictimRealAPI(t *testing.T) {
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
		for _, change := range []string{"none", "not terminating", "wrong owner", "old revision", "extra deficit", "companion deficit", terminatingVictimReplacement, terminatingVictimRevisionChange} {
			t.Run(fmt.Sprintf("%s/reader=%t", change, readReplica), func(t *testing.T) {
				f := newScaleDownResumeFixture(t, readReplica, 2)
				ns := fmt.Sprintf("terminating-resume-%d", index)
				index++
				require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
				createScaleDownResumeAPIFixture(t, live, f, ns)
				victim := scaleDownResumeVictim(t, f)
				require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(victim), victim))
				victim.Finalizers = []string{"test.openbao.org/hold"}
				if change == "wrong owner" {
					victim.OwnerReferences[0].UID = terminatingVictimForeignOwner
				}
				if change == "old revision" {
					victim.Labels[appsv1.StatefulSetRevisionLabel] = terminatingVictimStaleRevision
				}
				require.NoError(t, live.Update(t.Context(), victim))
				if change != "not terminating" {
					require.NoError(t, live.Delete(t.Context(), victim, client.Preconditions{UID: &victim.UID}))
				}
				f.sts.Status.CurrentReplicas--
				if change == "extra deficit" {
					f.sts.Status.CurrentReplicas--
				}
				require.NoError(t, live.Status().Update(t.Context(), f.sts))
				if change == "companion deficit" {
					for _, obj := range f.objects {
						if sts, ok := obj.(*appsv1.StatefulSet); ok && sts.Name != f.sts.Name {
							sts.Status.CurrentReplicas--
							require.NoError(t, live.Status().Update(t.Context(), sts))
						}
					}
				}
				f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
					if call == 2 && (change == terminatingVictimReplacement || change == terminatingVictimRevisionChange) {
						current := &corev1.Pod{}
						require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(victim), current))
						if change == terminatingVictimReplacement {
							current.Finalizers = nil
							require.NoError(t, live.Update(t.Context(), current))
							current.UID, current.ResourceVersion = "", ""
							current.DeletionTimestamp = nil
							current.DeletionGracePeriodSeconds = nil
							current.Finalizers = []string{"test.openbao.org/hold"}
							require.NoError(t, live.Create(t.Context(), current))
							require.NotEqual(t, victim.UID, current.UID)
							require.NoError(t, live.Delete(t.Context(), current, client.Preconditions{UID: &current.UID}))
						} else {
							current.Labels[appsv1.StatefulSetRevisionLabel] = terminatingVictimStaleRevision
							require.NoError(t, live.Update(t.Context(), current))
						}
					}
					return f.membership.config, nil
				}
				r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: live, APIReader: live}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
				resumed, resumeErr := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
				want := *f.sts.Spec.Replicas
				if change == "none" {
					require.NoError(t, resumeErr)
					require.True(t, resumed)
					want--
				} else {
					require.Error(t, resumeErr)
					require.False(t, resumed)
				}
				stored := &appsv1.StatefulSet{}
				require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(f.sts), stored))
				require.Equal(t, want, *stored.Spec.Replicas)
				require.Equal(t, f.sts.Spec.Template, stored.Spec.Template)
			})
		}
	}
}
