//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const backupKeyAnnotation = "openbao.org/backup-key"

// Manual-backup fixtures keep automatic execution outside the suite's duration.
// The dedicated scheduled source retains its real first-deadline witness.
func manualBackupSchedule(now time.Time) string {
	deadline := now.UTC().Add(12 * time.Hour).Truncate(time.Minute)
	return fmt.Sprintf("%d %d * * *", deadline.Minute(), deadline.Hour())
}

// backupObservation binds a backup to its source UID, a new Job UID, and its
// executor's object key. Earlier successful Jobs cannot satisfy this observation.
type backupObservation struct {
	cluster         *api.OpenBaoCluster
	previousJobUIDs map[types.UID]bool
	manualTrigger   string
}

func backupJobLabels(clusterName string) client.MatchingLabels {
	return client.MatchingLabels{
		constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoComponent:  "backup",
		constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
		constants.LabelOpenBaoCluster:    clusterName,
	}
}

func captureBackupObservation(ctx context.Context, c client.Client, cluster *api.OpenBaoCluster) (backupObservation, error) {
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs, client.InNamespace(cluster.Namespace), backupJobLabels(cluster.Name)); err != nil {
		return backupObservation{}, fmt.Errorf("list previous backup Jobs: %w", err)
	}
	observation := backupObservation{cluster: cluster.DeepCopy(), previousJobUIDs: make(map[types.UID]bool)}
	for i := range jobs.Items {
		observation.previousJobUIDs[jobs.Items[i].UID] = true
	}
	return observation, nil
}

func (o backupObservation) newJob(jobs []batchv1.Job) (*batchv1.Job, error) {
	var selected *batchv1.Job
	for i := range jobs {
		job := &jobs[i]
		if err := validateManagedLifecycleJobOwnerProof(job, o.cluster); err != nil {
			return nil, err
		}
		if o.previousJobUIDs[job.UID] {
			continue
		}
		if job.UID == "" || job.Namespace != o.cluster.Namespace || job.Annotations[backupKeyAnnotation] == "" {
			return nil, fmt.Errorf("new backup Job %s/%s lacks its UID, namespace, or object key", job.Namespace, job.Name)
		}
		if selected != nil {
			return nil, fmt.Errorf("multiple new backup Jobs exist for %s/%s", o.cluster.Namespace, o.cluster.Name)
		}
		selected = job.DeepCopy()
	}
	if selected == nil {
		return nil, fmt.Errorf("no new backup Job exists for %s/%s", o.cluster.Namespace, o.cluster.Name)
	}
	return selected, nil
}

func (o backupObservation) successfulJob(expected, current *batchv1.Job, cluster *api.OpenBaoCluster) error {
	if current.UID != expected.UID || current.Name != expected.Name || current.Namespace != expected.Namespace {
		return fmt.Errorf("backup Job identity changed while waiting for completion")
	}
	if err := validateManagedLifecycleJobOwnerProof(current, o.cluster); err != nil {
		return err
	}
	if current.Status.Succeeded == 0 {
		return fmt.Errorf("backup Job %s/%s has not succeeded", current.Namespace, current.Name)
	}
	key := expected.Annotations[backupKeyAnnotation]
	if key == "" || current.Annotations[backupKeyAnnotation] != key {
		return fmt.Errorf("backup Job object key is missing or changed")
	}
	if cluster.UID != o.cluster.UID || cluster.Status.Backup == nil || cluster.Status.Backup.LastBackupName != key {
		return fmt.Errorf("source cluster has not recorded this Job's successful backup")
	}
	if cluster.Status.Backup.LastHandledManualTrigger != o.manualTrigger {
		return fmt.Errorf("source cluster has not recorded the expected backup trigger")
	}
	// Callers can restore or remove the source next. Do not race the retention
	// executor, whose termination evidence must remain available to the controller.
	if cluster.Status.OperationLock != nil ||
		(cluster.Status.Backup.Retention != nil && cluster.Status.Backup.Retention.Active != nil) {
		return fmt.Errorf("backup or retention operation has not released the source")
	}
	return nil
}

func newScheduledBackupCluster(source *api.OpenBaoCluster, now time.Time) (*api.OpenBaoCluster, time.Time) {
	scheduled := source.DeepCopy()
	scheduled.ObjectMeta = metav1.ObjectMeta{Name: "s3-scheduled-cluster", Namespace: source.Namespace}
	scheduled.Status = api.OpenBaoClusterStatus{}
	scheduled.Spec.Replicas = 1
	scheduled.Spec.ReadReplicas = nil
	// Retain the real cron wait and leave time to observe its persisted deadline.
	deadline := now.UTC().Add(3 * time.Minute).Truncate(time.Minute)
	scheduled.Spec.Backup.Schedule = fmt.Sprintf("%d %d * * *", deadline.Minute(), deadline.Hour())
	return scheduled, deadline
}

func validateInitialBackupSchedule(cluster *api.OpenBaoCluster, deadline, now time.Time) error {
	if !now.Before(deadline) {
		return fmt.Errorf("first scheduled deadline passed before the initial observation")
	}
	b := cluster.Status.Backup
	if b == nil || b.NextScheduledBackup == nil || !b.NextScheduledBackup.Time.Equal(deadline) {
		return fmt.Errorf("first scheduled deadline has not been persisted")
	}
	if b.LastAttemptTime != nil || b.LastBackupName != "" || b.LastHandledManualTrigger != "" ||
		cluster.Annotations[constants.AnnotationTriggerBackup] != "" {
		return fmt.Errorf("scheduled fixture already has an attempt, backup, or manual trigger")
	}
	return nil
}

func validateScheduledBackupAttempt(cluster *api.OpenBaoCluster, deadline time.Time) error {
	b := cluster.Status.Backup
	if b == nil || b.LastAttemptTime == nil || b.LastAttemptTime.Time.Before(deadline) ||
		b.LastAttemptScheduledTime == nil || !b.LastAttemptScheduledTime.Time.Equal(deadline) {
		return fmt.Errorf("backup has not executed at the original scheduled deadline")
	}
	if b.LastHandledManualTrigger != "" || cluster.Annotations[constants.AnnotationTriggerBackup] != "" {
		return fmt.Errorf("scheduled fixture received a manual backup trigger")
	}
	return nil
}
