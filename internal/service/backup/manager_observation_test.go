package backup

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestObserveBackup_DoesNotMutateCluster(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	cluster := newTestClusterWithBackup("observation", "default")
	cluster.Spec.Backup.Schedule = "0 13 * * *"
	cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
	cluster.Status.Initialized = true
	cluster.Status.CurrentVersion = cluster.Spec.Version
	next := metav1.NewTime(now.Add(time.Hour))
	cluster.Status.Backup.NextScheduledBackup = &next

	manager := newBackupManager(newTestClient(t, cluster))
	original := cluster.DeepCopy()
	observation, err := manager.observeBackup(context.Background(), logr.Discard(), cluster, now)
	if err != nil {
		t.Fatalf("observeBackup() error = %v", err)
	}
	if !reflect.DeepEqual(cluster, original) {
		t.Fatal("observeBackup() mutated the cluster")
	}
	if observation.due {
		t.Fatal("due = true, want false")
	}
	if !observation.scheduledTime.Equal(next.Time) {
		t.Fatalf("scheduledTime = %v, want %v", observation.scheduledTime, next.Time)
	}
	if got := decideBackup(observation).kind; got != backupDecisionIdle {
		t.Fatalf("decision kind = %d, want %d", got, backupDecisionIdle)
	}
}

func TestObserveBackup_RefreshesDeadlineForCurrentSchedule(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 15, 0, 0, time.UTC)
	tests := []struct {
		name          string
		schedule      string
		persistedHour int
		wantHour      int
		wantDue       bool
		manual        bool
	}{
		{name: "earlier occurrence despite valid old deadline", schedule: "0 * * * *", persistedHour: 15, wantHour: 13},
		{name: "later schedule excludes old deadline", schedule: "0 18 * * *", persistedHour: 13, wantHour: 18},
		{name: "unchanged future deadline", schedule: "0 13 * * *", persistedHour: 13, wantHour: 13},
		{name: "unchanged overdue deadline", schedule: "0 12 * * *", persistedHour: 12, wantHour: 12, wantDue: true},
		{name: "valid overdue catch-up", schedule: "0 * * * *", persistedHour: 10, wantHour: 10, wantDue: true},
		{name: "invalid overdue deadline", schedule: "0 14 * * *", persistedHour: 12, wantHour: 14},
		{name: "manual trigger remains immediate", schedule: "0 * * * *", persistedHour: 15, wantHour: 13, wantDue: true, manual: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cluster := newTestClusterWithBackup("schedule-observation", "default")
			cluster.Spec.Backup.Schedule = test.schedule
			cluster.Spec.Backup.JWTAuthRole = testBackupJWTAuthRole
			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = cluster.Spec.Version
			cluster.Status.Backup.NextScheduledBackup = ptrToTime(time.Date(2026, 10, 23, test.persistedHour, 0, 0, 0, time.UTC))
			if test.manual {
				cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "request-1"}
			}
			manager := newBackupManager(newTestClient(t, cluster))
			original := cluster.DeepCopy()
			observation, err := manager.observeBackup(context.Background(), logr.Discard(), cluster, now)
			require.NoError(t, err)
			want := time.Date(2026, 10, 23, test.wantHour, 0, 0, 0, time.UTC)
			require.Equal(t, want, observation.initialNextSchedule)
			require.Equal(t, test.wantDue, observation.due)
			if test.manual {
				require.Equal(t, now, observation.scheduledTime)
				require.Equal(t, want, observation.nextSchedule)
			} else {
				require.Equal(t, want, observation.scheduledTime)
			}
			require.Equal(t, original, cluster, "observation must not mutate persisted status")
		})
	}
}

func TestObserveBackup_ActiveJobDefersManualTriggerEffect(t *testing.T) {
	t.Parallel()

	const triggerToken = "manual-1"

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	cluster := newTestClusterWithBackup("active-observation", "default")
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: triggerToken}
	job := newBackupJobForCluster(cluster, "active-backup", now.Add(-time.Minute))
	job.Status.Active = 1

	manager := newBackupManager(newTestClient(t, cluster, job))
	original := cluster.DeepCopy()
	observation, err := manager.observeBackup(context.Background(), logr.Discard(), cluster, now)
	if err != nil {
		t.Fatalf("observeBackup() error = %v", err)
	}
	if !reflect.DeepEqual(cluster, original) {
		t.Fatal("observeBackup() applied the manual trigger effect")
	}
	if observation.manualTriggerToken != triggerToken {
		t.Fatalf("manualTriggerToken = %q, want %s", observation.manualTriggerToken, triggerToken)
	}
	if got := decideBackup(observation).kind; got != backupDecisionObserve {
		t.Fatalf("decision kind = %d, want %d", got, backupDecisionObserve)
	}
}
