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
)

func TestScaleDownResumeNativeObservationsRealAPI(t *testing.T) {
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
		for _, terminating := range []bool{false, true} {
			for _, change := range scaleDownResumeNativeCases(readReplica) {
				t.Run(fmt.Sprintf("%s/reader=%t/terminating=%t", change, readReplica, terminating), func(t *testing.T) {
					f := newScaleDownResumeNativeFixture(t, readReplica, change)
					ns := fmt.Sprintf("native-resume-%d", index)
					index++
					require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
					createScaleDownResumeAPIFixture(t, live, f, ns)
					if terminating {
						victim := scaleDownResumeVictim(t, f)
						victim.Finalizers = []string{"test.openbao.org/native-hold"}
						require.NoError(t, live.Update(t.Context(), victim))
						require.NoError(t, live.Delete(t.Context(), victim, client.Preconditions{UID: &victim.UID}))
						f.sts.Status.CurrentReplicas--
						require.NoError(t, live.Status().Update(t.Context(), f.sts))
					}
					r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: live, APIReader: live}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
					resumed, resumeErr := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
					assertScaleDownResumeNativeResult(t, live, f, change, resumed, resumeErr)
				})
			}
		}
	}
}
