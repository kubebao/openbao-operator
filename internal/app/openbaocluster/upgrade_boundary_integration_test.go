//go:build integration

package openbaocluster

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

// Exercise the infrastructure entry point against real API objects. Envtest
// runs no StatefulSet controller; runtime Pod preservation has separate E2E coverage.
func TestUpgradeBoundaryPreservesWorkloadRealAPI(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	live, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for i, strategy := range []openbaov1alpha1.UpdateStrategyType{openbaov1alpha1.UpdateStrategyRollingUpdate, openbaov1alpha1.UpdateStrategyBlueGreen} {
		t.Run(string(strategy), func(t *testing.T) {
			ns := fmt.Sprintf("upgrade-boundary-%d", i)
			require.NoError(t, live.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: ns},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version: "2.5.5", Image: "openbao/openbao:2.5.5", Replicas: 3, Profile: openbaov1alpha1.ProfileDevelopment,
					TLS: openbaov1alpha1.TLSConfig{Enabled: true, RotationPeriod: "720h"}, Storage: openbaov1alpha1.StorageConfig{Size: "1Gi"},
					InitContainer: &openbaov1alpha1.InitContainerConfig{Image: "openbao/openbao-init:test"},
					Upgrade:       &openbaov1alpha1.UpgradeConfig{Strategy: strategy},
				},
			}
			require.NoError(t, live.Create(t.Context(), cluster))
			cluster.Status.Initialized, cluster.Status.CurrentVersion = true, "2.5.5"
			require.NoError(t, live.Status().Update(t.Context(), cluster))
			labels := map[string]string{"app": "bao"}
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: ns}, Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(3)), ServiceName: "bao", Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "bao", Image: "openbao/openbao:2.5.5"}}}},
			}}
			require.NoError(t, live.Create(t.Context(), sts))
			pods := make([]corev1.Pod, 3)
			for ordinal := range pods {
				pods[ordinal] = corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bao-%d", ordinal), Namespace: ns, Labels: labels}, Spec: sts.Spec.Template.Spec}
				require.NoError(t, live.Create(t.Context(), &pods[ordinal]))
			}
			cluster.Spec.Version, cluster.Spec.Image = "2.6.5", "openbao/openbao:2.6.5"
			require.NoError(t, live.Update(t.Context(), cluster))
			before := cluster.DeepCopy()
			r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: live, APIReader: live}}}
			_, err := r.Reconcile(t.Context(), logr.Discard(), cluster)
			require.ErrorIs(t, err, operatorerrors.ErrPermanentConfig)
			reason, ok := operatorerrors.Reason(err)
			require.True(t, ok)
			require.Equal(t, upgrade.ReasonUpgradeVersionIncompatible, reason)
			stored := &appsv1.StatefulSet{}
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(sts), stored))
			require.Equal(t, sts, stored, "rejection must not publish a new template or strategy")
			for _, pod := range pods {
				storedPod := &corev1.Pod{}
				require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(&pod), storedPod))
				require.Equal(t, pod.UID, storedPod.UID)
				require.Nil(t, storedPod.DeletionTimestamp)
			}
			storedCluster := &openbaov1alpha1.OpenBaoCluster{}
			require.NoError(t, live.Get(t.Context(), client.ObjectKeyFromObject(cluster), storedCluster))
			require.Equal(t, before, storedCluster, "rejection must not acquire a lock or start an upgrade")
			jobs := &batchv1.JobList{}
			require.NoError(t, live.List(t.Context(), jobs, client.InNamespace(ns)))
			require.Empty(t, jobs.Items)
			require.Contains(t, DefaultWorkloadResultPolicy().PermanentConfigurationReason, reason)
		})
	}
}
