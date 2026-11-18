package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

const backupAttemptAnnotation = "kubebao.org/backup-attempt"

// The Job carries its attempt receipt before it can execute. A new controller
// can recover the receipt if its creator stops before recording Cluster status.
type backupAttemptReceipt struct {
	Time          time.Time `json:"time"`
	ScheduledTime time.Time `json:"scheduledTime"`
	NextScheduled time.Time `json:"nextScheduled"`
	ManualTrigger string    `json:"manualTrigger,omitempty"`
}

func backupJobLockMessage(jobName string) string {
	return "backup job " + jobName
}

func (m *Manager) recoverBackupAttempt(ctx context.Context, cluster *api.OpenBaoCluster, observed *batchv1.Job) (*backupAttemptReceipt, error) {
	if observed == nil {
		return nil, nil
	}
	job := observed
	if observed.UID == "" {
		// The create path has only the deterministic name. Read its durable
		// receipt, including when Create returned AlreadyExists.
		var err error
		job, err = opslifecycle.ReadManagedJob(ctx, m.reader, client.ObjectKeyFromObject(observed),
			cluster, api.GroupVersion.WithKind("OpenBaoCluster"), "recover backup attempt")
		if err != nil {
			return nil, fmt.Errorf("read backup attempt Job: %w", err)
		}
	}
	// A terminal Job can be collected after the live list observation. Keep
	// that UID-bound completion evidence instead of requiring another read.
	if err := opslifecycle.RequireManagedJobOwner("recover backup attempt", job, cluster,
		api.GroupVersion.WithKind("OpenBaoCluster")); err != nil {
		return nil, err
	}
	raw := job.Annotations[backupAttemptAnnotation]
	if raw == "" {
		// Older operator versions did not write receipts. Keep their Jobs
		// observable without inventing an acknowledgement for a newer request.
		return nil, nil
	}
	var receipt backupAttemptReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return nil, fmt.Errorf("decode backup attempt receipt for Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	if receipt.Time.IsZero() || receipt.ScheduledTime.IsZero() || !receipt.NextScheduled.After(receipt.ScheduledTime) ||
		job.Name != backupJobName(cluster, receipt.ScheduledTime) {
		return nil, fmt.Errorf("invalid backup attempt receipt for Job %s/%s", job.Namespace, job.Name)
	}
	if !backupOperationLock.IsHeldBy(cluster.Status.OperationLock) ||
		cluster.Status.OperationLock.Message != backupJobLockMessage(job.Name) {
		return nil, fmt.Errorf("backup attempt Job %s/%s does not match the held operation", job.Namespace, job.Name)
	}
	if err := m.recordBackupAttempt(ctx, cluster, receipt.Time, receipt.ScheduledTime,
		receipt.NextScheduled, receipt.ManualTrigger); err != nil {
		return nil, fmt.Errorf("record backup attempt receipt for Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return &receipt, nil
}
