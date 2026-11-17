package backup

import (
	"context"
	"encoding/json"
	"regexp"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

var snapshotDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (m *Manager) snapshotSummary(ctx context.Context, job *batchv1.Job) (*api.BackupSnapshotSummary, error) {
	pods := &corev1.PodList{}
	if err := m.reader.List(ctx, pods, client.InNamespace(job.Namespace), client.MatchingLabels{batchv1.JobNameLabel: job.Name}); err != nil {
		return nil, err
	}
	var result *api.BackupSnapshotSummary
	for _, pod := range pods.Items {
		if !metav1.IsControlledBy(&pod, job) {
			continue
		}
		for _, container := range pod.Status.ContainerStatuses {
			term := container.State.Terminated
			if container.Name != ComponentBackup || term == nil || term.ExitCode != 0 || len(term.Message) > 4096 {
				continue
			}
			var report api.BackupSnapshotSummary
			if json.Unmarshal([]byte(term.Message), &report) != nil || !snapshotDigestPattern.MatchString(report.Digest) || report.Size <= 0 ||
				report.ClusterID == "" || len(report.ClusterID) > 128 || report.Version == "" || len(report.Version) > 64 {
				continue
			}
			if result != nil && *result != report {
				// Duplicate successful executors with conflicting observations cannot
				// provide an identity for unattended verification.
				return nil, nil
			}
			result = &report
		}
	}
	return result, nil
}
