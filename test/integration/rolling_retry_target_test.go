//go:build integration

package integration

import (
	"strconv"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/rolling"
)

func TestRollingRetryRequiresObservedRenderedTarget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		oldSource    bool
		unobserved   bool
		wrongImage   bool
		missingImage bool
	}{
		{name: "verified digest"},
		{name: "old source generation", oldSource: true},
		{name: "unobserved StatefulSet", unobserved: true},
		{name: "current observed wrong template image", wrongImage: true},
		{name: "missing resolved image", missingImage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namespace := newTestNamespace(t)
			cluster := newMinimalClusterObj(namespace, "retry-target")
			cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
				Enabled: true, FailurePolicy: constants.ImageVerificationFailurePolicyBlock,
			}
			cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
				JWTAuthRole: "upgrade", Requests: &openbaov1alpha1.UpgradeRequestConfig{Retry: "retry-now"},
			}
			require.NoError(t, k8sClient.Create(ctx, cluster))
			updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
				status.Initialized = true
				status.CurrentVersion = testPreviousOpenBaoVersion
			})
			controller := newControllerClient(t)
			mutateStatus := adminopsstatus.NewMutator(controller, controller)
			require.NoError(t, mutateStatus(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
				obj.Status.Upgrade = &openbaov1alpha1.UpgradeProgress{
					ExecutionID: "initial-execution",
					FromVersion: obj.Status.CurrentVersion, TargetVersion: obj.Spec.Version, CurrentPartition: 3,
					Failure: &openbaov1alpha1.ControllerErrorStatus{Reason: upgrade.ReasonPodNotReady},
				}
				return nil
			}, adminops.RespectOwnership))
			sourceGeneration := cluster.Generation
			if tc.oldSource {
				sourceGeneration--
			}
			labels := map[string]string{"app": cluster.Name}
			resolvedImage := "openbao/openbao@sha256:" + strings.Repeat("a", 64)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: cluster.Name, Namespace: namespace,
					Annotations: map[string]string{
						constants.AnnotationClusterGeneration: strconv.FormatInt(sourceGeneration, 10),
						constants.AnnotationResolvedMainImage: resolvedImage,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: cluster.Name, Replicas: ptr.To(int32(3)),
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name: constants.ContainerBao, Image: resolvedImage,
						}}},
					},
				},
			}
			if tc.missingImage {
				delete(sts.Annotations, constants.AnnotationResolvedMainImage)
			}
			require.NoError(t, k8sClient.Create(ctx, sts))
			if tc.wrongImage {
				// Image drift advances the StatefulSet generation but retains its
				// current cluster-generation and resolved-image annotations.
				sts.Spec.Template.Spec.Containers[0].Image = "openbao/openbao:old"
				require.NoError(t, k8sClient.Update(ctx, sts))
			}
			if !tc.unobserved {
				sts.Status.ObservedGeneration = sts.Generation
				sts.Status.UpdateRevision = "target"
				require.NoError(t, k8sClient.Status().Update(ctx, sts))
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: cluster.Name + "-2", Namespace: namespace,
					Labels:     map[string]string{appsv1.StatefulSetRevisionLabel: "old"},
					Finalizers: []string{"test.openbao.org/retain"},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: "openbao/openbao:old"}}},
			}
			require.NoError(t, k8sClient.Create(ctx, pod))
			manager := rolling.NewManager(controller, k8sScheme, nil, portopenbao.ClientConfig{}, nil, "").
				WithAdminOpsStatusMutator(mutateStatus)
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
			_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
			stored := &openbaov1alpha1.OpenBaoCluster{}
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), stored))
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod))
			if tc.oldSource || tc.unobserved || tc.wrongImage || tc.missingImage {
				require.ErrorIs(t, err, operatorerrors.ErrTransientKubernetesAPI)
				require.Nil(t, pod.DeletionTimestamp)
				require.NotNil(t, stored.Status.Upgrade.Failure)
				require.Equal(t, "initial-execution", stored.Status.Upgrade.ExecutionID)
				require.True(t, upgrade.RetryRequestPending(stored))
				return
			}
			// EnvTest has no StatefulSet or kubelet controller. The subsequent
			// quorum gate waits after the retry checkpoint and Pod deletion succeed.
			require.ErrorIs(t, err, operatorerrors.ErrTransientClusterState)
			require.NotNil(t, pod.DeletionTimestamp)
			require.Nil(t, stored.Status.Upgrade.Failure)
			require.NotEmpty(t, stored.Status.Upgrade.ExecutionID)
			require.NotEqual(t, "initial-execution", stored.Status.Upgrade.ExecutionID)
			require.NotNil(t, stored.Status.UpgradeRequests)
			require.Equal(t, "retry-now", stored.Status.UpgradeRequests.LastHandledRetry)
		})
	}
}
