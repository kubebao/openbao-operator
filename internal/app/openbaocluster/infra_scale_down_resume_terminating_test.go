package openbaocluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	terminatingVictimReplacement    = "replacement during observation"
	terminatingVictimRevisionChange = "revision during observation"
	terminatingVictimForeignOwner   = "foreign-victim-owner"
	terminatingVictimStaleRevision  = "stale-victim-revision"
)

func scaleDownResumeVictim(t *testing.T, f *scaleDownResumeFixture) *corev1.Pod {
	t.Helper()
	name := fmt.Sprintf("%s-%d", f.sts.Name, *f.sts.Spec.Replicas-1)
	for _, obj := range f.objects {
		if pod, ok := obj.(*corev1.Pod); ok && pod.Name == name {
			return pod
		}
	}
	require.FailNow(t, "missing scale-down victim fixture")
	return nil
}

func TestScaleDownResumeTerminatingVictim(t *testing.T) {
	for _, readReplica := range []bool{false, true} {
		for _, change := range []string{"none", "not terminating", "missing UID", "no controller owner", "wrong owner UID", "wrong owner name", "wrong owner kind", "wrong owner API", "old revision", "extra deficit", "companion deficit", terminatingVictimReplacement, terminatingVictimRevisionChange} {
			t.Run(fmt.Sprintf("%s/reader=%t", change, readReplica), func(t *testing.T) {
				f := newScaleDownResumeFixture(t, readReplica, 2)
				victim := scaleDownResumeVictim(t, f)
				victim.UID = "victim-uid"
				victim.Finalizers = []string{"test.openbao.org/hold"}
				now := metav1.Now()
				victim.DeletionTimestamp = &now
				f.sts.Status.CurrentReplicas--
				switch change {
				case "not terminating":
					victim.DeletionTimestamp = nil
				case "missing UID":
					victim.UID = ""
				case "no controller owner":
					victim.OwnerReferences = nil
				case "wrong owner UID":
					victim.OwnerReferences[0].UID = terminatingVictimForeignOwner
				case "wrong owner name":
					victim.OwnerReferences[0].Name = terminatingVictimForeignOwner
				case "wrong owner kind":
					victim.OwnerReferences[0].Kind = "Deployment"
				case "wrong owner API":
					victim.OwnerReferences[0].APIVersion = "other/v1"
				case "old revision":
					victim.Labels[appsv1.StatefulSetRevisionLabel] = terminatingVictimStaleRevision
				case "extra deficit":
					f.sts.Status.CurrentReplicas--
				case "companion deficit":
					for _, obj := range f.objects {
						if sts, ok := obj.(*appsv1.StatefulSet); ok && sts.Name != f.sts.Name {
							sts.Status.CurrentReplicas--
						}
					}
				}
				patches := 0
				c := f.client(t, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patches++
					return c.Patch(ctx, obj, patch, opts...)
				}})
				f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
					if call == 2 && (change == terminatingVictimReplacement || change == terminatingVictimRevisionChange) {
						pod := &corev1.Pod{}
						require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(victim), pod))
						if change == terminatingVictimReplacement {
							pod.UID = "replacement-uid"
						} else {
							pod.Labels[appsv1.StatefulSetRevisionLabel] = terminatingVictimStaleRevision
						}
						require.NoError(t, c.Update(t.Context(), pod))
					}
					return f.membership.config, nil
				}
				r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
				resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
				if change == "none" {
					require.NoError(t, err)
					require.True(t, resumed)
					require.Equal(t, 1, patches)
				} else {
					require.Error(t, err)
					require.False(t, resumed)
					require.Zero(t, patches)
				}
			})
		}
	}
}
