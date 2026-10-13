//go:build integration
// +build integration

package integration

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/adapter/security"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	"github.com/dc-tec/openbao-operator/internal/service/restore"
	"pgregory.net/rapid"
)

const defaultRestoreModelRapidChecks = "12"

type restoreLifecyclePath string

const (
	restoreLifecycleSuccess            restoreLifecyclePath = "success"
	restoreLifecycleFailedJob          restoreLifecyclePath = "failed_job"
	restoreLifecycleBlockedThenRelease restoreLifecyclePath = "blocked_then_release"
	restoreLifecycleForceOverride      restoreLifecyclePath = "force_override"
	restoreLifecycleRunningLockLost    restoreLifecyclePath = "running_lock_lost"
)

type restoreLifecycleScenario struct {
	Path                     restoreLifecyclePath
	NameSuffix               string
	DeleteAfterTerminal      bool
	TerminalReconcileRetries int
	RunningPollRetries       int
}

func TestRestoreLifecycleModelIntegration(t *testing.T) {
	setDefaultRestoreModelRapidChecks(t)

	rapid.Check(t, func(rt *rapid.T) {
		scenario := restoreLifecycleScenarioGenerator().Draw(rt, "scenario")
		runRestoreLifecycleModel(rt, scenario)
	})
}

func setDefaultRestoreModelRapidChecks(t *testing.T) {
	t.Helper()

	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "rapid.checks" {
			explicit = true
		}
	})
	if explicit {
		return
	}

	if err := flag.Set("rapid.checks", defaultRestoreModelRapidChecks); err != nil {
		t.Fatalf("set restore model rapid check count: %v", err)
	}
}

func runRestoreLifecycleModel(t *rapid.T, scenario restoreLifecycleScenario) {
	t.Helper()

	namespace := newRestoreModelNamespace(t)
	cluster := createRestoreModelCluster(t, namespace)
	restoreObj := createRestoreModelRequest(t, namespace, cluster.Name, scenario)
	controllerClient := newRestoreModelControllerClient(t)
	mgr := withIntegrationRestoreStatusPersistence(
		restore.NewManager(
			controllerClient,
			k8sScheme,
			nil,
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
		),
		controllerClient,
	)

	latest := getRestoreModelRequest(t, namespace, restoreObj.Name)
	reconcileRestoreModel(t, mgr, latest, "pending")
	latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
	assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseValidating)
	assertRestoreModelFinalizer(t, latest, true)
	if latest.Status.StartTime == nil {
		t.Fatalf("startTime = nil after pending reconcile")
	}
	if latest.Status.SnapshotKey != restoreObj.Spec.Source.Key {
		t.Fatalf("snapshotKey = %q, want %q", latest.Status.SnapshotKey, restoreObj.Spec.Source.Key)
	}

	switch scenario.Path {
	case restoreLifecycleBlockedThenRelease:
		setRestoreModelCompetingLock(t, cluster, openbaov1alpha1.ClusterOperationBackup)
		reconcileRestoreModel(t, mgr, latest, "validating with competing lock")
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseValidating)
		assertRestoreModelJobAbsent(t, namespace, latest.Name)
		assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationBackup, false)
		clearRestoreModelLock(t, cluster)
	case restoreLifecycleForceOverride:
		setRestoreModelCompetingLock(t, cluster, openbaov1alpha1.ClusterOperationBackup)
	}

	latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
	reconcileRestoreModel(t, mgr, latest, "validating")
	latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
	assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseRunning)
	assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationRestore, true)
	if scenario.Path == restoreLifecycleForceOverride {
		assertRestoreModelOverrideCondition(t, latest)
	}

	if scenario.Path == restoreLifecycleRunningLockLost {
		setRestoreModelCompetingLock(t, cluster, openbaov1alpha1.ClusterOperationUpgrade)
		reconcileRestoreModel(t, mgr, latest, "running after lock loss")
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelTerminal(t, latest, openbaov1alpha1.RestorePhaseFailed)
		assertRestoreModelJobAbsent(t, namespace, latest.Name)
		assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationUpgrade, false)
		reconcileRestoreModelTerminalRetries(t, mgr, latest, scenario.TerminalReconcileRetries)
		maybeDeleteRestoreModelRequest(t, mgr, namespace, latest.Name, scenario.DeleteAfterTerminal)
		return
	}

	reconcileRestoreModel(t, mgr, latest, "running creates job")
	latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
	assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseRunning)
	assertRestoreModelJobCount(t, namespace, latest.Name, 1)

	for i := 0; i < scenario.RunningPollRetries; i++ {
		reconcileRestoreModel(t, mgr, latest, fmt.Sprintf("running poll %d", i))
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseRunning)
		assertRestoreModelJobCount(t, namespace, latest.Name, 1)
	}

	switch scenario.Path {
	case restoreLifecycleFailedJob:
		markRestoreModelJobStatus(t, namespace, latest.Name, 0, 1)
		reconcileRestoreModel(t, mgr, latest, "failed job")
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelTerminal(t, latest, openbaov1alpha1.RestorePhaseFailed)
	case restoreLifecycleSuccess, restoreLifecycleBlockedThenRelease, restoreLifecycleForceOverride:
		markRestoreModelJobStatus(t, namespace, latest.Name, 1, 0)
		reconcileRestoreModel(t, mgr, latest, "successful job requests voter restart")
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelPhase(t, latest, openbaov1alpha1.RestorePhaseRunning)
		assertRestoreModelLock(t, namespace, cluster.Name, openbaov1alpha1.ClusterOperationRestore, true)
		cluster = getRestoreModelCluster(t, namespace, cluster.Name)
		if cluster.Status.Restore == nil || cluster.Status.Restore.UID != string(latest.UID) {
			t.Fatalf("cluster restore status = %+v, want UID %q", cluster.Status.Restore, latest.UID)
		}
		createSettledRestoreVoterStatefulSet(controllerClient, cluster, latest, t.Fatalf)
		reconcileRestoreModel(t, mgr, latest, "voter restart completed")
		latest = getRestoreModelRequest(t, namespace, restoreObj.Name)
		assertRestoreModelTerminal(t, latest, openbaov1alpha1.RestorePhaseCompleted)
	default:
		t.Fatalf("unexpected restore lifecycle path %q", scenario.Path)
	}

	assertRestoreModelLockReleased(t, namespace, cluster.Name)
	reconcileRestoreModelTerminalRetries(t, mgr, latest, scenario.TerminalReconcileRetries)
	maybeDeleteRestoreModelRequest(t, mgr, namespace, latest.Name, scenario.DeleteAfterTerminal)
}

func reconcileRestoreModel(
	t rapid.TB,
	mgr *restore.Manager,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	step string,
) {
	t.Helper()

	if _, err := mgr.Reconcile(ctx, logr.Discard(), restoreObj); err != nil {
		t.Fatalf("reconcile %s: %v", step, err)
	}
}

func reconcileRestoreModelTerminalRetries(
	t rapid.TB,
	mgr *restore.Manager,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	retries int,
) {
	t.Helper()

	phase := restoreObj.Status.Phase
	message := restoreObj.Status.Message
	completionTime := restoreObj.Status.CompletionTime
	for i := 0; i < retries; i++ {
		reconcileRestoreModel(t, mgr, restoreObj, fmt.Sprintf("terminal retry %d", i))
		latest := getRestoreModelRequest(t, restoreObj.Namespace, restoreObj.Name)
		assertRestoreModelPhase(t, latest, phase)
		if latest.Status.Message != message {
			t.Fatalf("terminal message changed from %q to %q", message, latest.Status.Message)
		}
		if completionTime == nil || latest.Status.CompletionTime == nil {
			t.Fatalf("completionTime changed unexpectedly: before=%v after=%v", completionTime, latest.Status.CompletionTime)
		}
		restoreObj = latest
	}
}

func maybeDeleteRestoreModelRequest(
	t rapid.TB,
	mgr *restore.Manager,
	namespace, name string,
	deleteAfterTerminal bool,
) {
	t.Helper()

	if !deleteAfterTerminal {
		return
	}

	latest := getRestoreModelRequest(t, namespace, name)
	if err := k8sClient.Delete(ctx, latest); err != nil {
		t.Fatalf("delete terminal restore: %v", err)
	}

	latest = getRestoreModelRequest(t, namespace, name)
	if latest.DeletionTimestamp == nil {
		t.Fatalf("deletionTimestamp = nil after deleting restore with finalizer")
	}
	reconcileRestoreModel(t, mgr, latest, "delete terminal restore")

	remaining := &openbaov1alpha1.OpenBaoRestore{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, remaining)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("get restore after first deletion reconcile: %v", err)
	}
	assertRestoreModelFinalizer(t, remaining, true)
	completeRestoreModelJobForegroundDeletion(t, namespace, name)
	reconcileRestoreModel(t, mgr, remaining, "delete terminal restore after Job deletion")

	deleted := &openbaov1alpha1.OpenBaoRestore{}
	err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deleted)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("restore still exists after Job deletion completed: %v", err)
	}
}

func completeRestoreModelJobForegroundDeletion(t rapid.TB, namespace, restoreName string) {
	t.Helper()

	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      restore.RestoreJobNamePrefix + restoreName,
	}, job)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("get restore Job after deletion request: %v", err)
	}
	if job.DeletionTimestamp.IsZero() {
		t.Fatalf("restore Job deletionTimestamp is zero after foreground deletion request")
	}

	finalizers := make([]string, 0, len(job.Finalizers))
	for _, finalizer := range job.Finalizers {
		if finalizer != metav1.FinalizerDeleteDependents {
			finalizers = append(finalizers, finalizer)
		}
	}
	job.Finalizers = finalizers
	garbageCollectorClient := newRestoreModelGarbageCollectorClient(t)
	if err := garbageCollectorClient.Update(ctx, job); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("complete restore Job foreground deletion: %v", err)
	}
}

func assertRestoreModelPhase(
	t rapid.TB,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	want openbaov1alpha1.RestorePhase,
) {
	t.Helper()

	if restoreObj.Status.Phase != want {
		t.Fatalf("phase = %s, want %s; status=%+v", restoreObj.Status.Phase, want, restoreObj.Status)
	}
}

func assertRestoreModelTerminal(
	t rapid.TB,
	restoreObj *openbaov1alpha1.OpenBaoRestore,
	want openbaov1alpha1.RestorePhase,
) {
	t.Helper()

	assertRestoreModelPhase(t, restoreObj, want)
	if restoreObj.Status.CompletionTime == nil {
		t.Fatalf("completionTime = nil for terminal phase %s", want)
	}
	if restoreObj.Status.Message == "" {
		t.Fatalf("terminal message is empty for phase %s", want)
	}
}

func assertRestoreModelFinalizer(t rapid.TB, restoreObj *openbaov1alpha1.OpenBaoRestore, want bool) {
	t.Helper()

	got := false
	for _, finalizer := range restoreObj.Finalizers {
		if finalizer == openbaov1alpha1.OpenBaoRestoreFinalizer {
			got = true
			break
		}
	}
	if got != want {
		t.Fatalf("restore finalizer present = %t, want %t; finalizers=%v", got, want, restoreObj.Finalizers)
	}
}

func assertRestoreModelOverrideCondition(t rapid.TB, restoreObj *openbaov1alpha1.OpenBaoRestore) {
	t.Helper()

	condition := meta.FindStatusCondition(restoreObj.Status.Conditions, constants.ConditionTypeOperationLockOverride)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("operation lock override condition = %+v, want true", condition)
	}
}

func assertRestoreModelJobAbsent(t rapid.TB, namespace, restoreName string) {
	t.Helper()

	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      restore.RestoreJobNamePrefix + restoreName,
	}, job)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("restore job existence error = %v, want not found", err)
	}
}

func assertRestoreModelJobCount(t rapid.TB, namespace, restoreName string, want int) {
	t.Helper()

	jobs := &batchv1.JobList{}
	if err := k8sClient.List(ctx, jobs, client.InNamespace(namespace)); err != nil {
		t.Fatalf("list restore jobs: %v", err)
	}
	got := 0
	for _, job := range jobs.Items {
		if strings.HasPrefix(job.Name, restore.RestoreJobNamePrefix+restoreName) {
			got++
		}
	}
	if got != want {
		t.Fatalf("restore job count = %d, want %d; jobs=%v", got, want, jobs.Items)
	}
}

func assertRestoreModelLock(
	t rapid.TB,
	namespace, clusterName string,
	wantOperation openbaov1alpha1.ClusterOperation,
	wantRestoreHolder bool,
) {
	t.Helper()

	cluster := getRestoreModelCluster(t, namespace, clusterName)
	if cluster.Status.OperationLock == nil {
		t.Fatalf("operationLock = nil, want operation %s", wantOperation)
	}
	if cluster.Status.OperationLock.Operation != wantOperation {
		t.Fatalf("operationLock.operation = %s, want %s", cluster.Status.OperationLock.Operation, wantOperation)
	}
	holderHasRestoreController := strings.HasPrefix(
		cluster.Status.OperationLock.Holder,
		constants.ControllerNameOpenBaoRestore+"/",
	)
	if holderHasRestoreController != wantRestoreHolder {
		t.Fatalf(
			"restore holder = %t, want %t; lock=%+v",
			holderHasRestoreController,
			wantRestoreHolder,
			cluster.Status.OperationLock,
		)
	}
}

func assertRestoreModelLockReleased(t rapid.TB, namespace, clusterName string) {
	t.Helper()

	cluster := getRestoreModelCluster(t, namespace, clusterName)
	if cluster.Status.OperationLock != nil {
		t.Fatalf("operationLock = %+v, want nil", cluster.Status.OperationLock)
	}
}

func newRestoreModelNamespace(t *rapid.T) string {
	t.Helper()

	name := fmt.Sprintf("it-restore-model-%d", time.Now().UnixNano())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), ns)
	})
	return name
}

func createRestoreModelCluster(t rapid.TB, namespace string) *openbaov1alpha1.OpenBaoCluster {
	t.Helper()

	cluster := newMinimalClusterObj(namespace, "restore-model-cluster")
	cluster.Spec.TLS.Enabled = false
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	cluster = getRestoreModelCluster(t, namespace, cluster.Name)
	updateRestoreModelClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
	})
	return getRestoreModelCluster(t, namespace, cluster.Name)
}

func createRestoreModelRequest(
	t rapid.TB,
	namespace, clusterName string,
	scenario restoreLifecycleScenario,
) *openbaov1alpha1.OpenBaoRestore {
	t.Helper()

	restoreName := "restore-" + strings.ToLower(strings.ReplaceAll(string(scenario.Path), "_", "-"))
	restoreName += "-" + scenario.NameSuffix
	restoreName = strings.ToLower(strings.ReplaceAll(restoreName, "_", "-"))
	if len(restoreName) > 50 {
		restoreName = restoreName[:50]
	}
	restoreObj := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreName,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: clusterName,
			Source: openbaov1alpha1.RestoreSource{
				Key: "snapshots/" + restoreName + ".snap",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://minio." + namespace + ".svc",
					Bucket:   testBackupBucket,
				},
			},
			Image:       "openbao-backup:dev",
			JWTAuthRole: "restore-role",
		},
	}
	if scenario.Path == restoreLifecycleForceOverride {
		restoreObj.Spec.Force = true
		restoreObj.Spec.OverrideOperationLock = true
	}
	if err := k8sClient.Create(ctx, restoreObj); err != nil {
		t.Fatalf("create OpenBaoRestore: %v", err)
	}
	return getRestoreModelRequest(t, namespace, restoreObj.Name)
}

func getRestoreModelRequest(t rapid.TB, namespace, name string) *openbaov1alpha1.OpenBaoRestore {
	t.Helper()

	restoreObj := &openbaov1alpha1.OpenBaoRestore{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, restoreObj); err != nil {
		t.Fatalf("get OpenBaoRestore %s/%s: %v", namespace, name, err)
	}
	return restoreObj
}

func getRestoreModelCluster(t rapid.TB, namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	t.Helper()

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cluster); err != nil {
		t.Fatalf("get OpenBaoCluster %s/%s: %v", namespace, name, err)
	}
	return cluster
}

func updateRestoreModelClusterStatus(
	t rapid.TB,
	cluster *openbaov1alpha1.OpenBaoCluster,
	mutate func(*openbaov1alpha1.OpenBaoClusterStatus),
) {
	t.Helper()

	latest := getRestoreModelCluster(t, cluster.Namespace, cluster.Name)
	mutate(&latest.Status)
	if err := k8sClient.Status().Update(ctx, latest); err != nil {
		t.Fatalf("update OpenBaoCluster status: %v", err)
	}
	cluster.Status = latest.Status
}

func setRestoreModelCompetingLock(
	t rapid.TB,
	cluster *openbaov1alpha1.OpenBaoCluster,
	operation openbaov1alpha1.ClusterOperation,
) {
	t.Helper()

	updateRestoreModelClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: operation,
			Holder:    constants.ControllerNameOpenBaoCluster + "/" + strings.ToLower(string(operation)),
			Message:   string(operation) + " operation",
		}
	})
}

func clearRestoreModelLock(t rapid.TB, cluster *openbaov1alpha1.OpenBaoCluster) {
	t.Helper()

	updateRestoreModelClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.OperationLock = nil
	})
}

func markRestoreModelJobStatus(t rapid.TB, namespace, restoreName string, succeeded, failed int32) {
	t.Helper()

	job := &batchv1.Job{}
	jobName := restore.RestoreJobNamePrefix + restoreName
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: jobName}, job); err != nil {
		t.Fatalf("get restore job %s/%s: %v", namespace, jobName, err)
	}
	setRestoreJobTerminalStatus(job, succeeded, failed)
	now := metav1.Now()
	job.Status.StartTime = &now
	if err := k8sClient.Status().Update(ctx, job); err != nil {
		t.Fatalf("update restore job status %s/%s: %v", namespace, jobName, err)
	}
}

func newRestoreModelControllerClient(t rapid.TB) client.Client {
	t.Helper()

	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{
		UserName: controllerUsername,
		Groups:   []string{"system:masters"},
	}
	c, err := client.New(impersonated, client.Options{Scheme: k8sScheme})
	if err != nil {
		t.Fatalf("create controller client: %v", err)
	}
	return c
}

func newRestoreModelGarbageCollectorClient(t rapid.TB) client.Client {
	t.Helper()

	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:kube-system:generic-garbage-collector",
		Groups:   []string{"system:masters", "system:serviceaccounts:kube-system"},
	}
	c, err := client.New(impersonated, client.Options{Scheme: k8sScheme})
	if err != nil {
		t.Fatalf("create garbage collector client: %v", err)
	}
	return c
}

func restoreLifecycleScenarioGenerator() *rapid.Generator[restoreLifecycleScenario] {
	return rapid.Custom(func(t *rapid.T) restoreLifecycleScenario {
		return restoreLifecycleScenario{
			Path: rapid.SampledFrom([]restoreLifecyclePath{
				restoreLifecycleSuccess,
				restoreLifecycleFailedJob,
				restoreLifecycleBlockedThenRelease,
				restoreLifecycleForceOverride,
				restoreLifecycleRunningLockLost,
			}).Draw(t, "path"),
			NameSuffix: rapid.StringMatching(`[a-z0-9]([a-z0-9-]{0,11}[a-z0-9])?`).
				Draw(t, "name_suffix"),
			DeleteAfterTerminal:      rapid.Bool().Draw(t, "delete_after_terminal"),
			TerminalReconcileRetries: rapid.IntRange(0, 2).Draw(t, "terminal_reconcile_retries"),
			RunningPollRetries:       rapid.IntRange(0, 2).Draw(t, "running_poll_retries"),
		}
	})
}
