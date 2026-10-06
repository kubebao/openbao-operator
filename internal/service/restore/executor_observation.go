package restore

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// restoreExecutorPending vetoes a terminal Job observation when its remaining
// Pods do not report terminated containers. It does not prove node fencing or
// server-side snapshot application. In particular, missing Pods are not process
// termination evidence for administrator recovery.
func (m *Manager) restoreExecutorPending(ctx context.Context, job *batchv1.Job) (string, error) {
	if job.UID == "" {
		return "", fmt.Errorf("restore Job UID is required for executor observation")
	}
	// Inspect ownership across the namespace: executor labels can be missing or
	// stale, and a same-name replacement Job must not inherit another Job's Pods.
	options := &client.ListOptions{Namespace: job.Namespace, Limit: 500}
	for {
		pods := &corev1.PodList{}
		if err := m.reader.List(ctx, pods, options); err != nil {
			return "", fmt.Errorf("list restore executor Pods: %w", err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.UID != job.UID {
				continue
			}
			if owner.APIVersion != batchv1.SchemeGroupVersion.String() || owner.Kind != "Job" || owner.Name != job.Name {
				return "", fmt.Errorf("restore executor Pod %s has inconsistent Job ownership", pod.Name)
			}
			if !restoreExecutorContainersTerminated(pod) {
				return fmt.Sprintf("Waiting for restore executor Pod %s to report termination of all containers.", pod.Name), nil
			}
		}
		if pods.Continue == "" {
			return "", nil
		}
		options.Continue = pods.Continue
	}
}

func restoreExecutorContainersTerminated(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	if len(pod.Spec.Containers) == 0 {
		return false
	}
	terminated := func(name string, statuses []corev1.ContainerStatus) bool {
		for _, status := range statuses {
			if status.Name == name {
				return status.State.Terminated != nil
			}
		}
		return false
	}
	for _, container := range pod.Spec.Containers {
		if !terminated(container.Name, pod.Status.ContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if !terminated(container.Name, pod.Status.InitContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if !terminated(container.Name, pod.Status.EphemeralContainerStatuses) {
			return false
		}
	}
	return true
}
