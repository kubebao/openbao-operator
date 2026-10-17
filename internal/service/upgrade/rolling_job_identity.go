package upgrade

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// RollingStepDownRunID scopes a Pod's leader transfer to the persisted rolling attempt.
// Legacy in-flight upgrades keep their existing names until an explicit retry.
func RollingStepDownRunID(cluster *openbaov1alpha1.OpenBaoCluster, podName string) string {
	if cluster != nil && cluster.Status.Upgrade != nil && cluster.Status.Upgrade.ExecutionID != "" {
		return cluster.Status.Upgrade.ExecutionID + "/" + podName
	}
	return podName
}

// ValidateRollingStepDownJob checks attempt provenance after managed ownership has been verified.
// A legacy Job without current-operation evidence requires an explicit retry.
func ValidateRollingStepDownJob(cluster *openbaov1alpha1.OpenBaoCluster, job *batchv1.Job, runID string) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return fmt.Errorf("rolling step-down Job requires upgrade progress")
	}
	if job.Annotations["openbao.org/upgrade-action"] != string(ExecutorActionRollingStepDownLeader) ||
		job.Annotations["openbao.org/upgrade-run-id"] != runID {
		return fmt.Errorf("rolling step-down Job %s has unexpected action or attempt identity; set %s to a new value to retry",
			job.Name, RequestRetryFieldPath)
	}
	progress := cluster.Status.Upgrade
	if progress.ExecutionID != "" {
		if !strings.HasPrefix(runID, progress.ExecutionID+"/") {
			return fmt.Errorf("rolling step-down Job %s does not belong to execution %s", job.Name, progress.ExecutionID)
		}
		return nil
	}
	if progress.StartedAt == nil || progress.StartedAt.IsZero() || job.CreationTimestamp.IsZero() ||
		job.CreationTimestamp.Before(progress.StartedAt) {
		return fmt.Errorf("legacy rolling step-down Job %s has no current-attempt provenance; set %s to a new value to retry",
			job.Name, RequestRetryFieldPath)
	}
	return nil
}
