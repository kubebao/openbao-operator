//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

func observedBackupFixture() (*api.OpenBaoCluster, *batchv1.Job) {
	cluster := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "tenant", UID: "source-uid"}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "backup", Namespace: cluster.Namespace, UID: "job-uid",
		Labels: backupJobLabels(cluster.Name),
		Annotations: map[string]string{
			constants.AnnotationOpenBaoOwnerUID: string(cluster.UID), backupKeyAnnotation: "snapshot-key",
		},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "OpenBaoCluster",
			Name: cluster.Name, UID: cluster.UID, Controller: ptr.To(true)}},
	}, Status: batchv1.JobStatus{Succeeded: 1}}
	cluster.Status.Backup = &api.BackupStatus{LastBackupName: "snapshot-key", LastHandledManualTrigger: "manual-token"}
	return cluster, job
}

func TestE2EFixtureBackupObservationIgnoresEarlierSuccess(t *testing.T) {
	cluster, old := observedBackupFixture()
	scheme := runtime.NewScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old).Build()
	observation, err := captureBackupObservation(t.Context(), c, cluster)
	require.NoError(t, err)
	_, err = observation.newJob([]batchv1.Job{*old})
	require.ErrorContains(t, err, "no new backup Job")
	current := old.DeepCopy()
	current.Name, current.UID = "new-backup", "new-job-uid"
	current.Status.Succeeded = 0
	selected, err := observation.newJob([]batchv1.Job{*old, *current})
	require.NoError(t, err)
	require.Equal(t, current.UID, selected.UID, "an older success must not mask a new pending Job")
	observation.manualTrigger = "manual-token"
	require.ErrorContains(t, observation.successfulJob(selected, current, cluster), "has not succeeded")
}

func TestE2EFixtureBackupObservationRequiresOwnedUnambiguousJob(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*batchv1.Job)
	}{
		{"missing UID", func(j *batchv1.Job) { j.UID = "" }},
		{"other namespace", func(j *batchv1.Job) { j.Namespace = "foreign-namespace" }},
		{"missing object key", func(j *batchv1.Job) { delete(j.Annotations, backupKeyAnnotation) }},
		{"wrong owner annotation", func(j *batchv1.Job) { j.Annotations[constants.AnnotationOpenBaoOwnerUID] = "old-source" }},
		{"wrong controller UID", func(j *batchv1.Job) { j.OwnerReferences[0].UID = "old-source" }},
		{"missing controller reference", func(j *batchv1.Job) { j.OwnerReferences = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster, job := observedBackupFixture()
			test.change(job)
			_, err := (backupObservation{cluster: cluster}).newJob([]batchv1.Job{*job})
			require.Error(t, err)
		})
	}
	cluster, first := observedBackupFixture()
	second := first.DeepCopy()
	second.Name, second.UID = "second", "second-uid"
	_, err := (backupObservation{cluster: cluster}).newJob([]batchv1.Job{*first, *second})
	require.ErrorContains(t, err, "multiple new backup Jobs")
}

func TestE2EFixtureBackupCompletionRequiresPinnedIdentityAndResult(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*batchv1.Job, *api.OpenBaoCluster)
	}{
		{"replacement Job with the same name", func(j *batchv1.Job, _ *api.OpenBaoCluster) { j.UID = "replacement" }},
		{"pending Job", func(j *batchv1.Job, _ *api.OpenBaoCluster) { j.Status.Succeeded = 0 }},
		{"changed object key", func(j *batchv1.Job, _ *api.OpenBaoCluster) { j.Annotations[backupKeyAnnotation] = "changed-key" }},
		{"stale source UID", func(_ *batchv1.Job, c *api.OpenBaoCluster) { c.UID = "replacement" }},
		{"stale backup result", func(_ *batchv1.Job, c *api.OpenBaoCluster) { c.Status.Backup.LastBackupName = "older-key" }},
		{"missing backup result", func(_ *batchv1.Job, c *api.OpenBaoCluster) { c.Status.Backup = nil }},
		{"another trigger", func(_ *batchv1.Job, c *api.OpenBaoCluster) {
			c.Status.Backup.LastHandledManualTrigger = "another-trigger"
		}},
		{"held source operation", func(_ *batchv1.Job, c *api.OpenBaoCluster) {
			c.Status.OperationLock = &api.OperationLockStatus{}
		}},
		{"active retention without a lock", func(_ *batchv1.Job, c *api.OpenBaoCluster) {
			c.Status.Backup.Retention = &api.BackupRetentionStatus{Active: &api.BackupRetentionRun{}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster, expected := observedBackupFixture()
			observation := backupObservation{cluster: cluster.DeepCopy(), manualTrigger: "manual-token"}
			current := expected.DeepCopy()
			require.NoError(t, observation.successfulJob(expected, current, cluster))
			test.change(current, cluster)
			require.Error(t, observation.successfulJob(expected, current, cluster))
		})
	}
}

func TestE2EFixtureScheduledBackupRetainsRealCronDelayAndFreshState(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 10, 15, 12, 10, 0, 0, time.FixedZone("local", 7200)),
		time.Date(2026, 12, 31, 23, 58, 59, 0, time.UTC),
	} {
		source, _ := observedBackupFixture()
		source.Annotations = map[string]string{constants.AnnotationTriggerBackup: "old-trigger"}
		source.Spec.Replicas = 3
		source.Spec.ReadReplicas = &api.ReadReplicaConfig{Replicas: 1}
		source.Spec.Backup = &api.BackupSchedule{Schedule: "*/5 * * * *"}
		scheduled, deadline := newScheduledBackupCluster(source, now)
		require.GreaterOrEqual(t, deadline.Sub(now), 2*time.Minute)
		require.LessOrEqual(t, deadline.Sub(now), 3*time.Minute)
		require.Empty(t, scheduled.UID)
		require.Empty(t, scheduled.Annotations)
		require.Nil(t, scheduled.Status.Backup)
		require.Equal(t, int32(1), scheduled.Spec.Replicas)
		require.Nil(t, scheduled.Spec.ReadReplicas)
		require.Equal(t, source.Namespace, scheduled.Namespace)
		schedule, err := backup.ParseSchedule(scheduled.Spec.Backup.Schedule)
		require.NoError(t, err)
		require.Equal(t, deadline, schedule.Next(now.UTC()), "the cron expression must retain the real deadline")
		scheduled.Spec.Backup.Schedule = "changed"
		require.Equal(t, "*/5 * * * *", source.Spec.Backup.Schedule, "fixture inputs must be independent")
		require.Equal(t, types.UID("source-uid"), source.UID)
	}
}

func TestE2EFixtureManualBackupsCannotRaceAnAutomaticWindow(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 10, 15, 11, 25, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("local", 7200)),
	} {
		schedule, err := backup.ParseSchedule(manualBackupSchedule(now))
		require.NoError(t, err)
		next := schedule.Next(now.UTC())
		require.Equal(t, now.UTC().Add(12*time.Hour).Truncate(time.Minute), next)
		require.Greater(t, next.Sub(now), time.Hour, "the 40-minute suite must not consume a scheduled window")
	}
}

func TestE2EFixtureScheduledBackupRequiresBeforeAndAfterDeadlineWitnesses(t *testing.T) {
	deadline := time.Date(2026, 10, 15, 12, 3, 0, 0, time.UTC)
	cluster := &api.OpenBaoCluster{Status: api.OpenBaoClusterStatus{Backup: &api.BackupStatus{
		NextScheduledBackup: ptr.To(metav1.NewTime(deadline)),
	}}}
	require.NoError(t, validateInitialBackupSchedule(cluster, deadline, deadline.Add(-time.Minute)))
	require.Error(t, validateInitialBackupSchedule(cluster, deadline, deadline))
	require.Error(t, validateInitialBackupSchedule(cluster, deadline.Add(time.Minute), deadline.Add(-time.Minute)))
	require.Error(t, validateScheduledBackupAttempt(cluster, deadline))
	cluster.Status.Backup.LastAttemptTime = ptr.To(metav1.NewTime(deadline.Add(-time.Second)))
	cluster.Status.Backup.LastAttemptScheduledTime = ptr.To(metav1.NewTime(deadline))
	require.Error(t, validateInitialBackupSchedule(cluster, deadline, deadline.Add(-time.Minute)))
	require.Error(t, validateScheduledBackupAttempt(cluster, deadline), "an early attempt cannot prove the real cron deadline")
	cluster.Status.Backup.LastAttemptTime = ptr.To(metav1.NewTime(deadline.Add(time.Second)))
	require.NoError(t, validateScheduledBackupAttempt(cluster, deadline))
	cluster.Status.Backup.LastAttemptScheduledTime = ptr.To(metav1.NewTime(deadline.Add(time.Minute)))
	require.Error(t, validateScheduledBackupAttempt(cluster, deadline), "the original persisted deadline must be used")
	cluster.Status.Backup.LastAttemptScheduledTime = ptr.To(metav1.NewTime(deadline))
	cluster.Status.Backup.LastHandledManualTrigger = "manual"
	require.Error(t, validateScheduledBackupAttempt(cluster, deadline))
	cluster.Status.Backup.LastHandledManualTrigger = ""
	cluster.Annotations = map[string]string{constants.AnnotationTriggerBackup: "manual"}
	require.Error(t, validateScheduledBackupAttempt(cluster, deadline))
}
