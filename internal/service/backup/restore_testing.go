package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

const restoreTestPoll = 30 * time.Second

func (m *Manager) reconcileRestoreTest(ctx context.Context, logger logr.Logger, cluster *api.OpenBaoCluster, now time.Time) error {
	if cluster.Status.Backup == nil {
		return nil
	}
	backup := cluster.Status.Backup
	expected := backup.DeepCopy()
	var config *api.RestoreTest
	if cluster.Spec.Backup != nil {
		config = cluster.Spec.Backup.RestoreTest
	}
	if backup.RestoreTest == nil {
		if config == nil {
			return nil
		}
		backup.RestoreTest = &api.RestoreTestStatus{}
	}
	status := backup.RestoreTest
	metrics := observability.NewClusterMetrics(cluster.Namespace, cluster.Name)
	if config == nil {
		metrics.ClearRestoreTest()
	} else {
		if status.Last != nil {
			value := 0.0
			if status.Last.Outcome == api.RestoreTestPassed {
				value = 1
			}
			metrics.SetRestoreTest(value)
		}
		lastSuccess := float64(0)
		if status.LastSuccessTime != nil {
			lastSuccess = float64(status.LastSuccessTime.Unix())
		}
		metrics.SetRestoreTestLastSuccess(lastSuccess)
	}
	if status.Active != nil {
		return m.observeRestoreTest(ctx, cluster, expected, now)
	}
	if config == nil {
		return nil
	}
	due, err := restoreTestDue(config, status, backup.SuccessfulBackups, cluster.CreationTimestamp.Time, now)
	if err != nil {
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "InvalidSchedule", "Restore test schedule is invalid")
	}
	if !due {
		return nil
	}
	if (config.CredentialsSecretRef == nil || config.CredentialsSecretRef.Name == "") && config.ServiceAccountName == "" {
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "StorageIdentityRequired",
			"Configure destination storage-reader credentials or a reader ServiceAccount; backup writer identities are not inherited")
	}
	snapshot := backup.LatestSnapshot
	if backup.LastBackupName == "" || snapshot == nil || snapshot.ClusterID == "" || snapshot.Version != config.ClusterTemplate.Version {
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "NoCompatibleSnapshot", "Waiting for a successful backup with a compatible source identity and version observation")
	}

	name := fmt.Sprintf("restore-test-%.12s-%d", cluster.UID, now.UnixNano())
	child := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   config.Namespace,
			Annotations: map[string]string{constants.AnnotationRestoreTestSource: string(cluster.UID)},
		},
		Spec: api.OpenBaoRestoreSpec{
			Cluster:             name,
			ClusterTemplate:     config.ClusterTemplate.DeepCopy(),
			TargetLifecycle:     api.RestoreTargetLifecycleDisposable,
			Force:               true,
			Image:               cluster.Spec.Backup.Image,
			CleanupAfterSeconds: config.CleanupAfterSeconds,
			ServiceAccountName:  config.ServiceAccountName,
			Source: api.RestoreSource{
				Key:               backup.LastBackupName,
				Target:            *cluster.Spec.Backup.Target.DeepCopy(),
				ExpectedClusterID: snapshot.ClusterID,
				ExpectedVersion:   snapshot.Version,
				ExpectedSize:      snapshot.Size,
				ExpectedDigest:    snapshot.Digest,
			},
		},
	}
	child.Spec.Source.Target.CredentialsSecretRef = config.CredentialsSecretRef.DeepCopy()
	child.Spec.Source.Target.RoleARN = config.RoleARN
	if child.Spec.Source.Target.S3 != nil {
		// A copied writer override must not hide the independent reader selection.
		child.Spec.Source.Target.S3.RoleARN = nil
	}
	child.Spec.Source.Target.WorkloadIdentity = config.WorkloadIdentity.DeepCopy()
	// Dry-run admission checks destination tenant policy before consuming a run.
	if err := m.client.Create(ctx, child, client.DryRunAll); err != nil {
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "DestinationRejected", "Destination admission rejected the restore test request; check namespace enrollment, restore-target approval, delegation, and referenced credentials")
	}
	child.UID, child.ResourceVersion, child.CreationTimestamp, child.ManagedFields = "", "", metav1.Time{}, nil
	stamp := metav1.NewTime(now)
	status.LastScheduledAt = &stamp
	status.LastBackupCount = backup.SuccessfulBackups
	status.Active = &api.RestoreTestRun{Namespace: config.Namespace, Name: name, Key: backup.LastBackupName, StartedAt: stamp}
	if err := m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionUnknown, "Running", "Disposable restore test is running"); err != nil {
		return err
	}
	if err := m.client.Create(ctx, child); err != nil {
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsBadRequest(err) || apierrors.IsUnauthorized(err) {
			expected = cluster.Status.Backup.DeepCopy()
			status = cluster.Status.Backup.RestoreTest
			status.Last = failedRestoreTestResult(status.Active, now, reasonRestoreTestDestinationRejected, "Destination admission rejected the reserved restore test request")
			status.Last.Digest = child.Spec.Source.ExpectedDigest
			status.Active = nil
			return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "DestinationRejected", "Destination rejected the reserved restore test request")
		}
		return fmt.Errorf("create reserved restore test request: %w", err)
	}
	logger.Info("Created disposable restore test", "namespace", child.Namespace, "restore", child.Name)
	return nil
}

func restoreTestDue(config *api.RestoreTest, status *api.RestoreTestStatus, successful int64, created, now time.Time) (bool, error) {
	if config.EverySuccessfulBackups > 0 {
		return successful-status.LastBackupCount >= config.EverySuccessfulBackups, nil
	}
	schedule, err := ParseSchedule(config.Schedule)
	if err != nil {
		return false, err
	}
	base := created
	if status.LastScheduledAt != nil {
		base = status.LastScheduledAt.Time
	}
	return !schedule.Next(base).After(now), nil
}

func (m *Manager) observeRestoreTest(ctx context.Context, cluster *api.OpenBaoCluster, expected *api.BackupStatus, now time.Time) error {
	status := cluster.Status.Backup.RestoreTest
	run := status.Active
	observed, err := readRestoreTestChild(ctx, m.reader, cluster)
	if err != nil {
		return err
	}
	if observed.reason != "" {
		if released, err := releaseRestoreTestReservation(ctx, m.adminOpsMutator, cluster); released || err != nil {
			return err
		}
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, observed.reason, observed.message)
	}
	child := observed.request
	if child == nil {
		return m.finishAbsentRestoreTest(ctx, cluster, expected, now)
	}
	if run.UID == "" {
		run.UID = child.UID
		return m.patchRestoreTestStatus(ctx, cluster, expected)
	}
	if child.Status.Target != nil && child.Status.Target.Cleanup == api.RestoreTargetCleanupFailed && child.Status.AdministratorDisposition != api.RestoreAdministratorAbandon {
		return m.restoreTestCondition(ctx, cluster, expected, metav1.ConditionFalse, "CleanupBlocked", "Disposable cleanup needs administrator inspection; no further restore test will start")
	}
	if child.Status.Phase != api.RestorePhaseCompleted && child.Status.Phase != api.RestorePhaseFailed {
		return nil
	}
	if child.Status.Target != nil && child.Status.Target.Cleanup != api.RestoreTargetCleanupComplete && child.Status.AdministratorDisposition != api.RestoreAdministratorAbandon {
		return nil
	}
	outcome := api.RestoreTestFailed
	condition := metav1.ConditionFalse
	if child.Status.Phase == api.RestorePhaseCompleted && child.Status.Target != nil && child.Status.Target.AppliedAt != nil {
		outcome, condition = api.RestoreTestPassed, metav1.ConditionTrue
	}
	// Persist the bounded result before deleting its only source of evidence.
	if status.Last == nil || status.Last.Name != run.Name {
		status.Last = summarizeRestoreTest(run, child, now)
		if outcome == api.RestoreTestPassed {
			status.LastSuccessTime = status.Last.FinishedAt.DeepCopy()
		}
		return m.restoreTestCondition(ctx, cluster, expected, condition, string(outcome), status.Last.Message)
	}
	if child.DeletionTimestamp == nil {
		return client.IgnoreNotFound(m.client.Delete(ctx, child, client.Preconditions{UID: &run.UID}))
	}
	return nil
}

// Only a previously bound request can finish through absence or replacement.
// An unbound reservation can still have an in-flight creation attempt.
func (m *Manager) finishAbsentRestoreTest(ctx context.Context, cluster *api.OpenBaoCluster, expected *api.BackupStatus, now time.Time) error {
	status := cluster.Status.Backup.RestoreTest
	if status.Last == nil || status.Last.Name != status.Active.Name {
		status.Last = failedRestoreTestResult(status.Active, now, reasonRestoreTestRequestMissing, "The bound request disappeared before its result was recorded")
	}
	status.Active = nil
	value := metav1.ConditionFalse
	if status.Last.Outcome == api.RestoreTestPassed {
		value = metav1.ConditionTrue
	}
	return m.restoreTestCondition(ctx, cluster, expected, value, string(status.Last.Outcome),
		"Bound restore test request is absent; any unobserved result is recorded as failed")
}

func (m *Manager) restoreTestCondition(ctx context.Context, cluster *api.OpenBaoCluster, expected *api.BackupStatus, value metav1.ConditionStatus, reason, message string) error {
	conditions := &cluster.Status.Backup.RestoreTest.Conditions
	current := meta.FindStatusCondition(*conditions, api.RestoreTestPassedConditionType)
	// Callers can also change reservation/result fields, so always persist those.
	if current == nil || current.Status != value || current.Reason != reason || current.ObservedGeneration != cluster.Generation {
		meta.SetStatusCondition(conditions, metav1.Condition{Type: api.RestoreTestPassedConditionType, Status: value, Reason: reason,
			Message: message, ObservedGeneration: cluster.Generation})
	}
	return m.patchRestoreTestStatus(ctx, cluster, expected)
}

// patchRestoreTestStatus compares the observed backup state before changing the
// reservation. A stale reconcile cannot replace another run or authorize Create.
func (m *Manager) patchRestoreTestStatus(ctx context.Context, cluster *api.OpenBaoCluster, expected *api.BackupStatus) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	reserving := cluster.Status.Backup.RestoreTest.Active != nil &&
		(expected.RestoreTest == nil || expected.RestoreTest.Active == nil)
	return m.adminOpsMutator(ctx, cluster, func(current *api.OpenBaoCluster) error {
		if reserving && current.DeletionTimestamp != nil {
			return fmt.Errorf("source is deleting; restore test cannot reserve a new request")
		}
		// New reservations depend on the observed spec. Finishing an existing run
		// depends on its identity and status, even if source deletion changed generation.
		if current.UID != cluster.UID || (reserving && current.Generation != cluster.Generation) ||
			(current.Status.Backup == nil ||
				!equality.Semantic.DeepEqual(current.Status.Backup.RestoreTest, expected.RestoreTest) ||
				current.Status.Backup.SuccessfulBackups != expected.SuccessfulBackups ||
				current.Status.Backup.LastBackupName != expected.LastBackupName ||
				!equality.Semantic.DeepEqual(current.Status.Backup.LatestSnapshot, expected.LatestSnapshot)) {
			return fmt.Errorf("backup changed before restore test status update; reconcile again")
		}
		current.Status.Backup.RestoreTest = cluster.Status.Backup.RestoreTest.DeepCopy()
		return nil
	}, adminops.ForceOwnershipOnConflict)
}
