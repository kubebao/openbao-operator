package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
)

const restoreTestSnapshotKey = "snapshot"
const restoreTestReaderAccountName = "snapshot-reader"

func TestRestoreTestDoesNotInheritBackupWriterIdentity(t *testing.T) {
	cluster := newRestoreTestSource("source")
	cluster.Spec.Backup.Target.RoleARN = "arn:aws:iam::123456789012:role/backup-writer"
	cluster.Spec.Backup.Target.WorkloadIdentity = &api.WorkloadIdentityConfig{
		ServiceAccountAnnotations: map[string]string{"azure.workload.identity/client-id": "writer"},
		PodLabels:                 map[string]string{"azure.workload.identity/use": "true"},
	}
	cluster.Spec.Backup.RestoreTest.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "storage-reader"}
	c := newTestClient(t, cluster)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
	children := &api.OpenBaoRestoreList{}
	require.NoError(t, c.List(t.Context(), children))
	require.Len(t, children.Items, 1)
	target := children.Items[0].Spec.Source.Target
	require.Empty(t, target.RoleARN, "restore tests must not inherit the backup writer's role")
	require.Nil(t, target.WorkloadIdentity, "destination credentials must not retain the writer's workload identity")
	require.Equal(t, "storage-reader", target.CredentialsSecretRef.Name)
	require.Equal(t, cluster.Spec.Backup.Target.Bucket, target.Bucket)
	require.Equal(t, "arn:aws:iam::123456789012:role/backup-writer", cluster.Spec.Backup.Target.RoleARN)
}

func TestRestoreTestPreservesPluginConfiguration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto-download-%t", enabled), func(t *testing.T) {
			cluster := newRestoreTestSource("source")
			template := &cluster.Spec.Backup.RestoreTest.ClusterTemplate
			template.Configuration = &api.RestoreTargetConfiguration{
				Plugin: &api.PluginConfig{AutoDownload: ptr.To(enabled)},
			}
			template.Plugins = []api.Plugin{{Type: "kms", Name: "custom-kms", Image: "registry.example.com/kms:v1.0.0"}}
			c := newTestClient(t, cluster)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
			children := &api.OpenBaoRestoreList{}
			require.NoError(t, c.List(t.Context(), children))
			require.Len(t, children.Items, 1)
			child := &children.Items[0]
			require.Equal(t, template.Configuration, child.Spec.ClusterTemplate.Configuration)
			require.Equal(t, template.Plugins, child.Spec.ClusterTemplate.Plugins)
			*child.Spec.ClusterTemplate.Configuration.Plugin.AutoDownload = !enabled
			require.Equal(t, enabled, *template.Configuration.Plugin.AutoDownload)
		})
	}
}

func TestRestoreTestUsesDestinationReaderIdentity(t *testing.T) {
	for _, identity := range []string{"S3 role", "workload identity", "existing account", "missing reader", "ambient backup"} {
		t.Run(identity, func(t *testing.T) {
			cluster := newRestoreTestSource("source")
			cluster.Spec.Backup.Target.RoleARN = "writer-role"
			config := cluster.Spec.Backup.RestoreTest
			config.CredentialsSecretRef = nil
			if identity == "ambient backup" {
				cluster.Spec.Backup.Target.RoleARN = ""
			}
			switch identity {
			case "S3 role":
				config.ServiceAccountName, config.RoleARN = restoreTestReaderAccountName, "reader-role"
			case "workload identity":
				config.ServiceAccountName = restoreTestReaderAccountName
				config.WorkloadIdentity = &api.WorkloadIdentityConfig{PodLabels: map[string]string{"azure.workload.identity/use": "true"}}
			case "existing account":
				config.ServiceAccountName = restoreTestReaderAccountName
			}
			c := newTestClient(t, cluster)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
			children := &api.OpenBaoRestoreList{}
			require.NoError(t, c.List(t.Context(), children))
			if identity == "missing reader" || identity == "ambient backup" {
				require.Empty(t, children.Items)
				require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
				require.Nil(t, cluster.Status.Backup.RestoreTest.LastScheduledAt)
				require.Equal(t, "StorageIdentityRequired", cluster.Status.Backup.RestoreTest.Conditions[0].Reason)
				return
			}
			require.Len(t, children.Items, 1)
			child := &children.Items[0]
			require.Equal(t, config.ServiceAccountName, child.Spec.ServiceAccountName)
			require.Equal(t, config.RoleARN, child.Spec.Source.Target.RoleARN)
			require.Equal(t, config.WorkloadIdentity, child.Spec.Source.Target.WorkloadIdentity)
		})
	}
}

func TestRestoreTestDue(t *testing.T) {
	created := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		config          api.RestoreTest
		count, previous int64
		last            *metav1.Time
		now             time.Time
		due, invalid    bool
	}{
		{"count not reached", api.RestoreTest{EverySuccessfulBackups: 3}, 2, 0, nil, created, false, false},
		{"count reached", api.RestoreTest{EverySuccessfulBackups: 3}, 3, 0, nil, created, true, false},
		{"restart does not repeat", api.RestoreTest{EverySuccessfulBackups: 3}, 3, 3, nil, created, false, false},
		{"next count", api.RestoreTest{EverySuccessfulBackups: 3}, 6, 3, nil, created, true, false},
		{"cron before", api.RestoreTest{Schedule: "0 2 * * *"}, 0, 0, nil, created.Add(time.Minute), false, false},
		{"cron due", api.RestoreTest{Schedule: "0 2 * * *"}, 0, 0, nil, created.Add(time.Hour), true, false},
		{"cron restart", api.RestoreTest{Schedule: "0 2 * * *"}, 0, 0, ptr.To(metav1.NewTime(created.Add(time.Hour))), created.Add(2 * time.Hour), false, false},
		{"bad cron", api.RestoreTest{Schedule: "bad"}, 0, 0, nil, created, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			due, err := restoreTestDue(&tc.config, &api.RestoreTestStatus{LastBackupCount: tc.previous, LastScheduledAt: tc.last}, tc.count, created, tc.now)
			require.Equal(t, tc.invalid, err != nil)
			require.Equal(t, tc.due, due)
		})
	}
}

func TestSnapshotSummaryRequiresOwnedSuccessfulExecutor(t *testing.T) {
	cluster := newTestClusterWithBackup("source", "test")
	job := newBackupJobForCluster(cluster, "backup", time.Now())
	good := api.BackupSnapshotSummary{ClusterID: "source-id", Version: "2.7.0", Size: 123, Digest: "sha256:" + strings.Repeat("a", 64)}
	for _, tc := range []string{"valid", "foreign owner", "failed container", "malformed", "oversized"} {
		t.Run(tc, func(t *testing.T) {
			data, err := json.Marshal(good)
			require.NoError(t, err)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: job.Namespace, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: ComponentBackup, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(data)}}}}}}
			switch tc {
			case "foreign owner":
				pod.OwnerReferences[0].UID = "foreign"
			case "failed container":
				pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
			case "malformed":
				pod.Status.ContainerStatuses[0].State.Terminated.Message = "not-json"
			case "oversized":
				pod.Status.ContainerStatuses[0].State.Terminated.Message = strings.Repeat("x", 4097)
			}
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(pod).Build()
			got, err := newBackupManager(c).snapshotSummary(t.Context(), job)
			require.NoError(t, err)
			if tc == "valid" {
				require.Equal(t, &good, got)
			} else {
				require.Nil(t, got)
			}
		})
	}
}

func TestRestoreTestReservationRejectsStaleWriter(t *testing.T) {
	cluster := newRestoreTestSource("source")
	c := newTestClient(t, cluster)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	stale := cluster.DeepCopy()
	m := newBackupManager(c)
	require.NoError(t, m.reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
	reserved := cluster.Status.Backup.RestoreTest.DeepCopy()
	require.ErrorContains(t, m.reconcileRestoreTest(t.Context(), logr.Discard(), stale, time.Now()), "backup changed")
	children := &api.OpenBaoRestoreList{}
	require.NoError(t, c.List(t.Context(), children))
	require.Len(t, children.Items, 1)

	// An ordinary backup status writer also preserves the current reservation.
	stale.Status.Backup.RestoreTest = nil
	require.NoError(t, m.patchStatusSSA(t.Context(), stale))
	require.Equal(t, reserved, stale.Status.Backup.RestoreTest)
}

func TestRestoreTestLostReservationAcknowledgementDoesNotCreate(t *testing.T) {
	cluster := newRestoreTestSource("source")
	lost := false
	c := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&api.OpenBaoCluster{}).WithObjects(cluster).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceApply: func(ctx context.Context, c client.Client, subResource string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
				if err := c.SubResource(subResource).Apply(ctx, obj, opts...); err != nil {
					return err
				}
				if !lost {
					lost = true
					return fmt.Errorf("lost acknowledgement")
				}
				return nil
			},
		}).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	m := newBackupManager(c)
	require.ErrorContains(t, m.reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()), "lost acknowledgement")

	// Restart observes the reservation but never retries its creation attempt.
	for i := 0; i < 2; i++ {
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
		require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
	}
	require.NotNil(t, cluster.Status.Backup.RestoreTest.Active)
	require.Equal(t, "RequestMissing", cluster.Status.Backup.RestoreTest.Conditions[0].Reason)
	children := &api.OpenBaoRestoreList{}
	require.NoError(t, c.List(t.Context(), children))
	require.Empty(t, children.Items)
}

func TestRestoreTestCreationRejectionReleasesOnlyDefinitiveReservations(t *testing.T) {
	resource := schema.GroupResource{Group: "openbao.org", Resource: "openbaorestores"}
	for _, tc := range []struct {
		name     string
		err      error
		released bool
	}{
		{"forbidden", apierrors.NewForbidden(resource, "child", fmt.Errorf("denied")), true},
		{"bad request", apierrors.NewBadRequest("invalid"), true},
		{"unauthorized", apierrors.NewUnauthorized("expired"), true},
		{"collision", apierrors.NewAlreadyExists(resource, "child"), false},
		{"lost response", apierrors.NewTimeoutError("unknown result", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newRestoreTestSource("source")
			previousSuccess := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
			cluster.Status.Backup.RestoreTest = &api.RestoreTestStatus{LastSuccessTime: &previousSuccess}
			base := newTestClient(t, cluster)
			c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
				options := &client.CreateOptions{}
				for _, option := range opts {
					option.ApplyToCreate(options)
				}
				if len(options.DryRun) != 0 {
					return nil
				}
				return tc.err
			}})
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			err := newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now())
			if tc.released {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.Equal(t, tc.released, cluster.Status.Backup.RestoreTest.Active == nil)
			require.Equal(t, &previousSuccess, cluster.Status.Backup.RestoreTest.LastSuccessTime)
			if tc.released {
				require.Equal(t, api.RestoreTestFailed, cluster.Status.Backup.RestoreTest.Last.Outcome)
			}
		})
	}
}

func TestRestoreTestRequestAbsencePreservesCreationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		bound, replaced, recorded bool
	}{
		{name: "unbound absent"},
		{name: "unbound collision", replaced: true},
		{name: "bound absent", bound: true},
		{name: "bound replaced", bound: true, replaced: true},
		{name: "recorded result", bound: true, replaced: true, recorded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newTestClusterWithBackup("source", "test")
			run := &api.RestoreTestRun{Name: "child", Namespace: "recovery"}
			if tc.bound {
				run.UID = "original"
			}
			cluster.Status.Backup.RestoreTest = &api.RestoreTestStatus{Active: run}
			status := cluster.Status.Backup.RestoreTest
			if tc.recorded {
				status.Last = &api.RestoreTestResult{Name: run.Name, Outcome: api.RestoreTestPassed}
			}
			child := &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: run.Name, UID: "replacement"}}
			c := newTestClient(t, cluster)
			if tc.replaced {
				require.NoError(t, c.Create(t.Context(), child))
			}
			require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
			require.Equal(t, tc.bound, status.Active == nil)
			if tc.bound {
				outcome := api.RestoreTestFailed
				if tc.recorded {
					outcome = "Passed"
				}
				require.Equal(t, outcome, status.Last.Outcome)
			} else {
				require.Nil(t, status.Last)
			}
			if tc.replaced {
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(child), child))
				require.Nil(t, child.DeletionTimestamp)
				require.Empty(t, child.Annotations)
			}
		})
	}
}

func TestDisabledRestoreTestClearsMetricsAndOnlyPollsActiveRun(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			cluster := newTestClusterWithBackup(fmt.Sprintf("disabled-restore-test-%t", active), "test")
			cluster.Spec.Backup = nil
			stamp := metav1.NewTime(time.Unix(1000, 0))
			cluster.Status.Backup.RestoreTest = &api.RestoreTestStatus{
				LastSuccessTime: &stamp,
				Last:            &api.RestoreTestResult{Name: "finished", FinishedAt: stamp, Outcome: api.RestoreTestPassed},
			}
			if active {
				cluster.Status.Backup.RestoreTest.Active = &api.RestoreTestRun{Name: "uncertain", Namespace: "recovery"}
			}
			metrics := observability.NewClusterMetrics(cluster.Namespace, cluster.Name)
			metrics.SetRestoreTest(1)
			metrics.SetRestoreTestLastSuccess(float64(stamp.Unix()))
			t.Cleanup(metrics.ClearRestoreTest)
			manager := newBackupManager(newTestClient(t, cluster))
			result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			if active {
				require.Equal(t, restoreTestPoll, result.RequeueAfter)
				require.Equal(t, "RequestMissing", cluster.Status.Backup.RestoreTest.Conditions[0].Reason)
			} else {
				require.Zero(t, result.RequeueAfter)
			}
			require.Equal(t, &stamp, cluster.Status.Backup.RestoreTest.LastSuccessTime)
			gathered, err := controllermetrics.Registry.Gather()
			require.NoError(t, err)
			for _, family := range gathered {
				if family.GetName() != "openbao_restore_test_success" && family.GetName() != "openbao_restore_test_last_success_timestamp_seconds" {
					continue
				}
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						if label.GetName() == "name" {
							require.NotEqual(t, cluster.Name, label.GetValue())
						}
					}
				}
			}
		})
	}
}

func TestRestoreTestCannotReserveAfterSourceDeletionBegins(t *testing.T) {
	cluster := newRestoreTestSource("deleting-source")
	cluster.Finalizers = []string{api.OpenBaoClusterFinalizer}
	c := newTestClient(t, cluster)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	stale := cluster.DeepCopy()
	require.NoError(t, c.Delete(t.Context(), cluster))
	require.ErrorContains(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), stale, time.Now()), "source is deleting")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.Nil(t, cluster.Status.Backup.RestoreTest)
	children := &api.OpenBaoRestoreList{}
	require.NoError(t, c.List(t.Context(), children))
	require.Empty(t, children.Items)
}

func TestRestoreTestGenerationChangeOnlyBlocksNewReservations(t *testing.T) {
	for _, reserving := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish rejected run", true: "reserve new run"}[reserving], func(t *testing.T) {
			cluster := newTestClusterWithBackup("source", "test")
			cluster.Generation = 1
			cluster.Status.Backup.RestoreTest = &api.RestoreTestStatus{}
			run := &api.RestoreTestRun{Name: "child", Namespace: "recovery"}
			c := newTestClient(t, cluster)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			manager := newBackupManager(c)
			if !reserving {
				expected := cluster.Status.Backup.DeepCopy()
				cluster.Status.Backup.RestoreTest.Active = run
				require.NoError(t, manager.patchRestoreTestStatus(t.Context(), cluster, expected))
			}
			stale := cluster.DeepCopy()
			expected := stale.Status.Backup.DeepCopy()
			cluster.Generation++
			require.NoError(t, c.Update(t.Context(), cluster))
			stale.Status.Backup.RestoreTest.Active = nil
			if reserving {
				stale.Status.Backup.RestoreTest.Active = run
			} else {
				stale.Status.Backup.RestoreTest.Last = &api.RestoreTestResult{Name: run.Name, Outcome: api.RestoreTestFailed}
			}

			err := manager.patchRestoreTestStatus(t.Context(), stale, expected)
			if reserving {
				require.ErrorContains(t, err, "backup changed")
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
		})
	}
}

func newRestoreTestSource(name string) *api.OpenBaoCluster {
	cluster := newTestClusterWithBackup(name, "test")
	cluster.Status.Backup.SuccessfulBackups = 1
	cluster.Status.Backup.LastBackupName = restoreTestSnapshotKey
	cluster.Status.Backup.LatestSnapshot = &api.BackupSnapshotSummary{
		ClusterID: "source",
		Version:   "2.7.0",
	}
	cluster.Spec.Backup.RestoreTest = &api.RestoreTest{
		EverySuccessfulBackups: 1,
		Namespace:              "destination",
		CredentialsSecretRef:   &corev1.LocalObjectReference{Name: "storage-reader"},
		ClusterTemplate:        api.RestoreClusterTemplate{Version: "2.7.0"},
	}
	return cluster
}
