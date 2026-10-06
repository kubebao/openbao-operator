package restore

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
)

func TestObserveRestore_DoesNotMutateRestore(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenBao API to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch API to scheme: %v", err)
	}

	restore := newRunningRestoreForObservation()
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	restore.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCreated
	job := managedRestoreJobForRestore(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      restore.Status.Execution.JobName,
		Namespace: restore.Namespace,
		UID:       types.UID("restore-job-uid"),
	}}, restore)
	job.Annotations[restoreExecutionIDAnnotation] = restore.Status.Execution.OperationID
	restore.Status.Execution.JobUID = job.UID
	cluster := newRestoreObservationCluster(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		Build()
	manager := &Manager{reader: k8sClient}
	original := restore.DeepCopy()

	observation, err := manager.observeRestore(context.Background(), restore)
	if err != nil {
		t.Fatalf("observeRestore() error = %v", err)
	}
	if !reflect.DeepEqual(restore, original) {
		t.Fatal("observeRestore() mutated the restore")
	}
	if observation.job == nil || observation.job.Name != job.Name {
		t.Fatalf("observed Job = %v, want %s", observation.job, job.Name)
	}
	if observation.state.jobState != restoreJobRunning {
		t.Fatalf("job state = %d, want %d", observation.state.jobState, restoreJobRunning)
	}
	if got := decideRestore(observation.state).kind; got != restoreDecisionPollJob {
		t.Fatalf("decision kind = %d, want %d", got, restoreDecisionPollJob)
	}
}

func TestObserveRestore_LegacyJobDefersAdoption(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenBao API to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch API to scheme: %v", err)
	}

	restore := newRunningRestoreForObservation()
	job := managedRestoreJobForRestore(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      restoreJobName(restore),
		Namespace: restore.Namespace,
		UID:       types.UID("legacy-restore-job-uid"),
	}}, restore)
	cluster := newRestoreObservationCluster(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore, job).
		Build()
	manager := &Manager{reader: k8sClient}

	observation, err := manager.observeRestore(context.Background(), restore)
	if err != nil {
		t.Fatalf("observeRestore() error = %v", err)
	}
	if restore.Status.Execution != nil {
		t.Fatal("observeRestore() adopted the legacy Job")
	}
	if !observation.state.legacy {
		t.Fatal("legacy = false, want true")
	}
	if got := decideRestore(observation.state).kind; got != restoreDecisionAdoptLegacyJob {
		t.Fatalf("decision kind = %d, want %d", got, restoreDecisionAdoptLegacyJob)
	}
}

func TestObserveRestore_MissingCommittedJobBlocksRecreation(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenBao API to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch API to scheme: %v", err)
	}

	restore := newRunningRestoreForObservation()
	restore.Status.Execution = newRestoreExecutionStatus(restore)
	restore.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCommitted
	cluster := newRestoreObservationCluster(restore)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, restore).
		Build()
	manager := &Manager{reader: k8sClient}

	observation, err := manager.observeRestore(context.Background(), restore)
	if err != nil {
		t.Fatalf("observeRestore() error = %v", err)
	}
	decision := decideRestore(observation.state)
	if decision.kind != restoreDecisionMarkUnknown {
		t.Fatalf("decision kind = %d, want %d", decision.kind, restoreDecisionMarkUnknown)
	}
	if !strings.Contains(decision.message, "operator will not recreate it") {
		t.Fatalf("decision message = %q, want no-recreation guidance", decision.message)
	}
}

func newRunningRestoreForObservation() *openbaov1alpha1.OpenBaoRestore {
	return &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "observation-restore",
			Namespace: "default",
			UID:       types.UID("observation-restore-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "observation-cluster"},
		Status: openbaov1alpha1.OpenBaoRestoreStatus{
			Phase: openbaov1alpha1.RestorePhaseRunning,
		},
	}
}

func newRestoreObservationCluster(restore *openbaov1alpha1.OpenBaoRestore) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name:      restore.Spec.Cluster,
		Namespace: restore.Namespace,
		UID:       types.UID("observation-cluster-uid"),
	}}
}

func TestClassifyRestoreJob_RequiresTerminalConditions(t *testing.T) {
	t.Parallel()
	one := int32(1)
	complete := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	failed := batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}
	for _, tt := range []struct {
		name   string
		status batchv1.JobStatus
		want   restoreJobState
	}{
		{"empty", batchv1.JobStatus{}, restoreJobRunning},
		{"succeeded counter", batchv1.JobStatus{Succeeded: 1}, restoreJobRunning},
		{"failed counter", batchv1.JobStatus{Failed: 1}, restoreJobRunning},
		{"failed pod and active replacement", batchv1.JobStatus{Failed: 1, Active: 1}, restoreJobRunning},
		{"success criteria met", batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue}}}, restoreJobRunning},
		{"failure target", batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue}}}, restoreJobRunning},
		{"false condition", batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}}}, restoreJobRunning},
		{"complete", batchv1.JobStatus{Succeeded: 1, Failed: 1, Conditions: []batchv1.JobCondition{complete}}, restoreJobSucceeded},
		{"failed", batchv1.JobStatus{Conditions: []batchv1.JobCondition{failed}}, restoreJobFailed},
		{"conflicting terminal conditions", batchv1.JobStatus{Conditions: []batchv1.JobCondition{complete, failed}}, restoreJobRunning},
		{"complete with active pod", batchv1.JobStatus{Active: 1, Conditions: []batchv1.JobCondition{complete}}, restoreJobRunning},
		{"failed with terminating pod", batchv1.JobStatus{Terminating: &one, Conditions: []batchv1.JobCondition{failed}}, restoreJobRunning},
	} {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, classifyRestoreJob(&batchv1.Job{Status: tt.status})) })
	}
	require.Equal(t, restoreJobRunning, classifyRestoreJob(nil))
}
