package rolling

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func TestResetRolloutPodsForRetryUsesCurrentRenderedTarget(t *testing.T) {
	t.Parallel()
	const tag = "openbao/openbao:2.6.2"
	const digest = "openbao/openbao@sha256:verified"
	for _, tc := range []struct {
		name             string
		specImage        string
		templateImage    string
		resolvedImage    string
		missingImage     bool
		podImage         string
		sourceGeneration string
		observed         int64
		verification     bool
		staleRevision    bool
		wait             bool
		deletePod        bool
	}{
		{name: "verified tag to digest", specImage: tag, templateImage: digest, podImage: digest, sourceGeneration: "2", observed: 7, verification: true, staleRevision: true, deletePod: true},
		{name: "verified digest input", specImage: digest, templateImage: digest, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, verification: true, deletePod: true},
		{name: "verification disabled", specImage: tag, templateImage: tag, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, deletePod: true},
		{name: "default image", templateImage: tag, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, deletePod: true},
		{name: "verification Warn fallback", specImage: tag, templateImage: tag, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, verification: true, deletePod: true},
		{name: "current rendered digest Pod preserved", specImage: tag, templateImage: digest, podImage: digest, sourceGeneration: "2", observed: 7, verification: true},
		{name: "same revision wrong Pod image", specImage: tag, templateImage: digest, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, verification: true, deletePod: true},
		{name: "old template wrong image", specImage: tag, templateImage: "openbao/openbao:old", podImage: "openbao/openbao:old", sourceGeneration: "1", observed: 7, wait: true},
		{name: "old template matching image", specImage: tag, templateImage: tag, podImage: "openbao/openbao:old", sourceGeneration: "1", observed: 7, wait: true},
		{name: "missing source generation", specImage: tag, templateImage: tag, podImage: "openbao/openbao:old", observed: 7, wait: true},
		{name: "unobserved rendered digest", specImage: tag, templateImage: digest, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 6, verification: true, wait: true},
		{name: "current observed wrong template image", specImage: tag, templateImage: "openbao/openbao:old", resolvedImage: digest, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, verification: true, wait: true},
		{name: "missing resolved image", specImage: tag, templateImage: digest, podImage: "openbao/openbao:old", sourceGeneration: "2", observed: 7, verification: true, missingImage: true, wait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "retry", Namespace: "default", Generation: 2},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Image: tc.specImage, Version: "2.6.2", Replicas: 3,
					ImageVerification: &openbaov1alpha1.ImageVerificationConfig{Enabled: tc.verification},
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{Upgrade: &openbaov1alpha1.UpgradeProgress{CurrentPartition: 3}},
			}
			resolvedImage := tc.resolvedImage
			if resolvedImage == "" && !tc.missingImage {
				resolvedImage = tc.templateImage
			}
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: cluster.Name, Namespace: cluster.Namespace, Generation: 7,
					Annotations: map[string]string{
						constants.AnnotationClusterGeneration: tc.sourceGeneration,
						constants.AnnotationResolvedMainImage: resolvedImage,
					},
				},
				Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.ContainerBao, Image: tc.templateImage}},
				}}},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: tc.observed, UpdateRevision: "target"},
			}
			pod := retryPodForResetTest(cluster.Namespace, "retry-2", "target", tc.podImage, true)
			if tc.staleRevision {
				pod.Labels[appsv1.StatefulSetRevisionLabel] = "old"
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
			manager := &Manager{client: c, reader: c}
			err := manager.resetRolloutPodsForRetry(t.Context(), logr.Discard(), cluster)
			if tc.wait {
				require.ErrorIs(t, err, operatorerrors.ErrTransientKubernetesAPI)
			} else {
				require.NoError(t, err)
			}
			err = c.Get(t.Context(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
			if tc.deletePod {
				require.True(t, apierrors.IsNotFound(err), "expected stale Pod deletion, got %v", err)
			} else {
				require.NoError(t, err, "wait and matching-target paths must preserve the Pod")
			}
		})
	}
}
