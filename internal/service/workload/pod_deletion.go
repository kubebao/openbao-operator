package workload

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// CompleteTerminatedPodDeletion completes at most one already-requested deletion.
// It never starts deletion, removes finalizers, or deletes a nonterminal Pod.
func (m *Manager) CompleteTerminatedPodDeletion(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (bool, error) {
	if !terminalPodCleanupEnabled(cluster) {
		return false, nil
	}
	var pods corev1.PodList
	if err := m.reader.List(ctx, &pods, client.InNamespace(cluster.Namespace), client.MatchingLabels{
		constants.LabelOpenBaoCluster: cluster.Name,
		constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
	}); err != nil {
		return false, fmt.Errorf("list Pods for terminal deletion cleanup: %w", err)
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for i := range pods.Items {
		candidate := &pods.Items[i]
		if !terminalPodDeletionEligible(candidate) {
			continue
		}
		requested, err := m.completeTerminatedPodDeletion(ctx, logger, cluster, candidate)
		if err != nil || requested {
			return requested, err
		}
	}
	return false, nil
}

func terminalPodCleanupEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil && cluster.Name != "" && cluster.Namespace != "" && cluster.UID != "" && cluster.DeletionTimestamp.IsZero() &&
		!cluster.Spec.Paused && cluster.Spec.Profile != "" && cluster.Annotations[constants.AnnotationRestoreHold] == ""
}

func terminalPodDeletionEligible(pod *corev1.Pod) bool {
	if pod.UID == "" || pod.ResourceVersion == "" || pod.DeletionTimestamp.IsZero() || len(pod.Finalizers) != 0 ||
		(pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded) ||
		pod.Labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
		pod.Labels[constants.LabelOpenBaoComponent] != constants.ComponentOpenBaoCluster {
		return false
	}
	for _, statuses := range [][]corev1.ContainerStatus{
		pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses,
	} {
		for _, status := range statuses {
			if status.State.Running != nil {
				return false
			}
		}
	}
	return true
}

func (m *Manager) completeTerminatedPodDeletion(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, candidate *corev1.Pod) (bool, error) {
	pod := &corev1.Pod{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(candidate), pod); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("read current Pod for terminal deletion cleanup: %w", err))
	}
	if pod.UID != candidate.UID || !terminalPodDeletionEligible(pod) || pod.Labels[constants.LabelOpenBaoCluster] != cluster.Name {
		return false, nil
	}
	owner := metav1.GetControllerOfNoCopy(pod)
	if owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "StatefulSet" || owner.UID == "" {
		return false, nil
	}
	sts := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner.Name}, sts); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("read Pod owner for terminal deletion cleanup: %w", err))
	}
	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(cluster), current); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("read cluster authority for terminal deletion cleanup: %w", err))
	}
	if current.UID != cluster.UID || !terminalPodCleanupEnabled(current) || sts.UID != owner.UID ||
		sts.Labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
		sts.Labels[constants.LabelOpenBaoCluster] != current.Name ||
		!resourceownership.HasManagedControllerOwnerProof(sts, current, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")) {
		return false, nil
	}
	zero := int64(0)
	if err := m.client.Delete(ctx, pod, client.GracePeriodSeconds(zero), client.Preconditions{
		UID: &pod.UID, ResourceVersion: &pod.ResourceVersion,
	}); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("complete terminated Pod deletion: %w", err))
	}
	logger.Info("Requested completion of terminated Pod deletion", "pod", pod.Name, "pod_uid", pod.UID)
	return true, nil
}
