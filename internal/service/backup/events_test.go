package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func expectEventContains(t *testing.T, recorder *events.FakeRecorder, parts ...string) {
	t.Helper()

	select {
	case event := <-recorder.Events:
		for _, part := range parts {
			if !strings.Contains(event, part) {
				t.Fatalf("event %q does not contain %q", event, part)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("expected event, got none")
	}
}

func expectAnyEventContains(t *testing.T, recorder *events.FakeRecorder, attempts int, parts ...string) {
	t.Helper()

	for i := 0; i < attempts; i++ {
		select {
		case event := <-recorder.Events:
			match := true
			for _, part := range parts {
				if !strings.Contains(event, part) {
					match = false
					break
				}
			}
			if match {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("expected event, got none")
		}
	}

	t.Fatalf("did not find event containing %q within %d attempts", strings.Join(parts, ", "), attempts)
}

func TestRecordManualTriggerAccepted_EmitsAcceptedEvent(t *testing.T) {
	t.Parallel()

	cluster := newTestClusterWithBackup("manual-events", "backup-ns")
	const manualTriggerToken = "now"
	cluster.Annotations = map[string]string{"openbao.org/trigger-backup": manualTriggerToken}

	recorder := events.NewFakeRecorder(10)
	k8sClient := newTestClient(t, cluster)
	manager := withTestAdminOpsStatusPersistence(
		NewManager(
			k8sClient,
			testScheme,
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		),
		k8sClient,
	)

	manager.recordManualTriggerAccepted(logr.Discard(), cluster, manualTriggerToken)

	expectEventContains(t, recorder, "Normal", ReasonBackupManualTriggerAccepted)
}

func TestProcessBackupJobResult_EmitsCompletedEvent(t *testing.T) {
	t.Parallel()

	cluster := newTestClusterWithBackup("backup-events-success", "default")
	scheduled := time.Date(2025, 1, 15, 3, 0, 0, 0, time.UTC)
	jobName := backupJobName(cluster, scheduled)
	job := managedBackupJobForCluster(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				"openbao.org/backup-key": "test-key-abc",
			},
		},
		Status: batchv1.JobStatus{
			Succeeded: 1,
		},
	}, cluster)

	recorder := events.NewFakeRecorder(10)
	k8sClient := newTestClient(t, cluster, job)
	manager := withTestAdminOpsStatusPersistence(
		NewManager(
			k8sClient,
			testScheme,
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		),
		k8sClient,
	)

	result, err := manager.processBackupJobResult(context.Background(), logr.Discard(), cluster, jobName)
	if err != nil {
		t.Fatalf("processBackupJobResult() error = %v", err)
	}
	if !result.statusUpdated {
		t.Fatal("statusUpdated = false, want true")
	}

	expectEventContains(t, recorder, "Normal", ReasonBackupCompleted)
}

func TestProcessBackupJobResult_EmitsFailedEvent(t *testing.T) {
	t.Parallel()

	cluster := newTestClusterWithBackup("backup-events-failed", "default")
	scheduled := time.Date(2025, 1, 15, 3, 0, 0, 0, time.UTC)
	jobName := backupJobName(cluster, scheduled)
	job := managedBackupJobForCluster(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cluster.Namespace,
		},
		Status: batchv1.JobStatus{
			Failed: 1,
		},
	}, cluster)

	recorder := events.NewFakeRecorder(10)
	k8sClient := newTestClient(t, cluster, job)
	manager := withTestAdminOpsStatusPersistence(
		NewManager(
			k8sClient,
			testScheme,
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		),
		k8sClient,
	)

	result, err := manager.processBackupJobResult(context.Background(), logr.Discard(), cluster, jobName)
	if err != nil {
		t.Fatalf("processBackupJobResult() error = %v", err)
	}
	if !result.statusUpdated {
		t.Fatal("statusUpdated = false, want true")
	}

	expectEventContains(t, recorder, "Warning", ReasonBackupFailed)
}

func TestEnsureBackupJob_EmitsIdentityConfigurationEvent(t *testing.T) {
	t.Parallel()

	cluster := newTestClusterWithBackup("backup-identity", "default")
	scheduled := time.Date(2025, 1, 15, 3, 0, 0, 0, time.UTC)

	recorder := events.NewFakeRecorder(10)
	k8sClient := newTestClient(t, cluster)
	manager := withTestAdminOpsStatusPersistence(
		NewManager(
			k8sClient,
			testScheme,
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		),
		k8sClient,
	)

	inProgress, err := manager.ensureBackupJob(context.Background(), logr.Discard(), cluster, backupJobName(cluster, scheduled), testBackupAttempt(scheduled))
	if err != nil {
		t.Fatalf("ensureBackupJob() error = %v", err)
	}
	if !inProgress {
		t.Fatal("inProgress = false, want true")
	}

	expectAnyEventContains(t, recorder, 2, "Normal", ReasonBackupIdentityConfiguration)
	expectAnyEventContains(t, recorder, 2, "Normal", ReasonBackupJobCreated)
}
