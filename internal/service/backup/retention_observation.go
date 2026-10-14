package backup

import (
	"context"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// observeRetentionPods refuses to infer stopped execution from Job counters,
// deletion, or missing Pods. Every observed executor must be terminated.
func (m *Manager) observeRetentionPods(ctx context.Context, job *batchv1.Job, run *api.BackupRetentionRun) (bool, *RetentionReport, error) {
	pods := &corev1.PodList{}
	// Pod labels are mutable. Scan the namespace and select controller owner UIDs.
	if err := m.reader.List(ctx, pods, client.InNamespace(job.Namespace)); err != nil {
		return false, nil, err
	}
	owned := 0
	conflicting := false
	var report *RetentionReport
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOfNoCopy(&pod)
		if owner == nil || owner.UID != job.UID {
			continue
		}
		if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != job.Name {
			return false, nil, nil
		}
		owned++
		if !retentionPodTerminated(&pod) {
			return false, nil, nil
		}
		for _, container := range pod.Status.ContainerStatuses {
			term := container.State.Terminated
			if container.Name != retentionComponent || term == nil || len(term.Message) > 4096 {
				continue
			}
			var candidate RetentionReport
			if json.Unmarshal([]byte(term.Message), &candidate) != nil || candidate.SourceJobUID != string(run.SourceJobUID) ||
				candidate.Key != run.Key || candidate.DeletedByCount < 0 || candidate.DeletedByAge < 0 ||
				(candidate.Succeeded && term.ExitCode != 0) || (!candidate.Succeeded && term.ExitCode == 0) {
				continue
			}
			if report != nil && *report != candidate {
				conflicting = true
			}
			report = &candidate
		}
	}
	if conflicting {
		return owned > 0, nil, fmt.Errorf("retention executors reported conflicting outcomes")
	}
	return owned > 0, report, nil
}

func retentionPodTerminated(pod *corev1.Pod) bool {
	if len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) || len(pod.Spec.Containers) == 0 ||
		len(pod.Status.InitContainerStatuses) != len(pod.Spec.InitContainers) ||
		len(pod.Status.EphemeralContainerStatuses) != len(pod.Spec.EphemeralContainers) {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if !retentionContainerTerminated(container.Name, pod.Status.ContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if !retentionContainerTerminated(container.Name, pod.Status.InitContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if !retentionContainerTerminated(container.Name, pod.Status.EphemeralContainerStatuses) {
			return false
		}
	}
	return true
}

func retentionContainerTerminated(name string, statuses []corev1.ContainerStatus) bool {
	for _, status := range statuses {
		if status.Name == name {
			return status.State.Terminated != nil
		}
	}
	return false
}
