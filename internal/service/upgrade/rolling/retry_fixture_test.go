package rolling

import (
	"context"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Rendering and event tests isolate Raft IO. retry_raft_test uses the real guard.
type renderingRetryGuard struct{}

func (renderingRetryGuard) CheckVoterRollout(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, string, bool) error {
	return nil
}

func adoptRetryResetObjects(cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet, objects ...client.Object) {
	if cluster.Spec.Replicas < 1 {
		cluster.Spec.Replicas = 1
		if progress := cluster.Status.Upgrade; progress != nil {
			cluster.Spec.Replicas = max(cluster.Spec.Replicas, progress.CurrentPartition)
			for _, ordinal := range progress.CompletedPods {
				cluster.Spec.Replicas = max(cluster.Spec.Replicas, ordinal+1)
			}
		}
	}
	if cluster.UID == "" {
		cluster.UID = types.UID(cluster.Name + "-cluster")
	}
	if sts.UID == "" {
		sts.UID = types.UID(sts.Name + "-set")
	}
	sts.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}
	if sts.Spec.Replicas == nil {
		sts.Spec.Replicas = ptr.To(max(int32(1), cluster.Spec.Replicas))
	}
	for _, obj := range objects {
		if obj.GetUID() == "" {
			obj.SetUID(types.UID(obj.GetName() + "-uid"))
		}
		if pod, ok := obj.(*corev1.Pod); ok {
			pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}
		} else {
			obj.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))})
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[constants.AnnotationOpenBaoOwnerUID] = string(cluster.UID)
			obj.SetAnnotations(annotations)
		}
	}
}
