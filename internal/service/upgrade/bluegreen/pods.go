package bluegreen

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// isPodReady checks if a pod is in Ready condition.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func countActivePods(pods []corev1.Pod) int {
	activePods := 0
	for _, pod := range pods {
		if pod.DeletionTimestamp == nil {
			activePods++
		}
	}
	return activePods
}

// getPodsByRevision returns all pods belonging to the specified revision.
// This is a unified helper used for both Blue and Green pod lookup.
func (m *Manager) getPodsByRevision(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, rev string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	selectorLabels := map[string]string{
		constants.LabelAppInstance: cluster.Name,
		constants.LabelAppName:     constants.LabelValueAppNameOpenBao,
	}
	if rev != "" {
		selectorLabels[constants.LabelOpenBaoRevision] = rev
	} else if cluster.Status.BlueGreen != nil && cluster.Status.BlueGreen.BlueControllerRevision != "" {
		selectorLabels[appsv1.ControllerRevisionHashLabelKey] = cluster.Status.BlueGreen.BlueControllerRevision
	}
	labelSelector := labels.SelectorFromSet(selectorLabels)

	if err := m.client.List(ctx, podList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	); err != nil {
		return nil, fmt.Errorf("failed to list pods for revision %s: %w", rev, err)
	}

	if rev != "" || (cluster.Status.BlueGreen != nil && cluster.Status.BlueGreen.BlueControllerRevision != "") {
		return podList.Items, nil
	}

	// Compatibility fallback for status written before BlueControllerRevision
	// existed: only Pods from the original unrevisioned StatefulSet are Blue.
	pods := make([]corev1.Pod, 0, len(podList.Items))
	prefix := cluster.Name + "-"
	for i := range podList.Items {
		pod := podList.Items[i]
		if strings.HasPrefix(pod.Name, prefix) && pod.Labels[constants.LabelOpenBaoRevision] == "" {
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

// cleanupGreenStatefulSet removes any lingering Green StatefulSet.
func (m *Manager) cleanupGreenStatefulSet(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster.Status.BlueGreen == nil || cluster.Status.BlueGreen.GreenRevision == "" {
		return nil
	}

	greenRevision := cluster.Status.BlueGreen.GreenRevision
	greenStatefulSet := &appsv1.StatefulSet{}
	if err := m.client.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      fmt.Sprintf("%s-%s", cluster.Name, greenRevision),
	}, greenStatefulSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to get Green StatefulSet: %w", err)
	}

	if err := m.client.Delete(ctx, greenStatefulSet, client.Preconditions{UID: &greenStatefulSet.UID}); err != nil {
		return fmt.Errorf("failed to delete Green StatefulSet: %w", err)
	}

	logger.Info("Cleaned up Green StatefulSet", "greenRevision", greenRevision)
	return nil
}
