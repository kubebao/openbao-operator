package perf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func restoreRecoveryFixture() (*api.OpenBaoRestore, *api.OpenBaoCluster) {
	request := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "perf", UID: "restore-uid", Generation: 1,
			Labels: map[string]string{perfRunIDLabel: "run"}},
		Spec: api.OpenBaoRestoreSpec{Cluster: "cluster"},
		Status: api.OpenBaoRestoreStatus{
			Phase: api.RestorePhaseUnknown,
			Execution: &api.RestoreExecutionStatus{
				OperationID: "restore-uid", TargetUID: "cluster-uid", JobUID: "job-uid",
				Stage: api.RestoreExecutionStageUnknown, TerminalResult: api.RestoreExecutionResultSucceeded,
				TerminalObservedAt: &metav1.Time{Time: time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC)},
			},
			SubmissionClaim: &api.RestoreSubmissionClaim{PodUID: "executor-uid"},
		},
	}
	cluster := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name: "cluster", Namespace: "perf", UID: "cluster-uid",
		Labels:      map[string]string{perfRunIDLabel: "run"},
		Annotations: map[string]string{constants.AnnotationRestoreHold: "restore-uid"},
	}}
	return request, cluster
}

func TestRestoreRecoveryRejectsUncertainAndUnownedFixtures(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*api.OpenBaoRestore, *api.OpenBaoCluster){
		"missing receipt": func(r *api.OpenBaoRestore, _ *api.OpenBaoCluster) { r.Status.Execution = nil },
		"unknown result":  func(r *api.OpenBaoRestore, _ *api.OpenBaoCluster) { r.Status.Execution.TerminalResult = "" },
		"failed result": func(r *api.OpenBaoRestore, _ *api.OpenBaoCluster) {
			r.Status.Execution.TerminalResult = api.RestoreExecutionResultFailed
		},
		"missing claim":   func(r *api.OpenBaoRestore, _ *api.OpenBaoCluster) { r.Status.SubmissionClaim = nil },
		"unowned request": func(r *api.OpenBaoRestore, _ *api.OpenBaoCluster) { r.Labels = nil },
		"unowned target":  func(_ *api.OpenBaoRestore, c *api.OpenBaoCluster) { c.Labels = nil },
		"replaced target": func(_ *api.OpenBaoRestore, c *api.OpenBaoCluster) { c.UID = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, c := restoreRecoveryFixture()
			mutate(r, c)
			require.Error(t, validateFixtureRestoreReceipt(r, c, "run"))
			require.Empty(t, r.Annotations[constants.AnnotationRestoreAcknowledge])
		})
	}
}

func TestRestoreRecoveryAcknowledgesOwnedFixtureAndWaitsForReplacement(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	request, cluster := restoreRecoveryFixture()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(request, cluster).
		WithStatusSubresource(&api.OpenBaoRestore{}, &api.OpenBaoCluster{}).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(request), request))
	native := &nativeScenarioContext{client: c, namespace: "perf", runID: "run"}
	var phases []Phase
	phaseTimes := map[string]time.Time{
		"restore_requested": request.Status.Execution.TerminalObservedAt.Add(-5 * time.Second),
	}
	done, err := native.resumeFixtureRestore(t.Context(), request, cluster, &phases, phaseTimes)
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(request), request))
	require.Equal(t, "restore-uid/Resume", request.Annotations[constants.AnnotationRestoreAcknowledge])
	require.Equal(t, api.RestorePhaseUnknown, request.Status.Phase)
	require.NotZero(t, phaseTimes["restore_submission_accepted"])
	require.NotZero(t, phaseTimes["restore_recovery_requested"])

	request.Status.AdministratorDisposition = api.RestoreAdministratorResume
	request.Status.Restart = &api.RestoreRestartStatus{
		Pods: []api.RestoreRestartPod{{Name: "cluster-0", UID: "original-pod"}}, CompletedAt: &metav1.Time{Time: time.Now()},
	}
	request.Status.Conditions = []metav1.Condition{{Type: constants.RestoreRecoveryReleasedConditionType,
		Status: metav1.ConditionTrue, ObservedGeneration: request.Generation, LastTransitionTime: metav1.Now()}}
	delete(cluster.Annotations, constants.AnnotationRestoreHold)
	require.NoError(t, c.Update(t.Context(), cluster))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cluster-0", Namespace: "perf", UID: "original-pod"},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(),
		}}}}
	require.NoError(t, c.Create(t.Context(), pod))
	done, err = native.resumeFixtureRestore(t.Context(), request, cluster, &phases, phaseTimes)
	require.NoError(t, err)
	require.False(t, done, "the original Pod must not satisfy recovery completion")
	require.NoError(t, c.Delete(t.Context(), pod))
	pod.ResourceVersion = ""
	pod.UID = "replacement-pod"
	require.NoError(t, c.Create(t.Context(), pod))
	done, err = native.resumeFixtureRestore(t.Context(), request, cluster, &phases, phaseTimes)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, api.RestorePhaseUnknown, request.Status.Phase)
	require.NotZero(t, phaseTimes["restore_recovery_released"])
	measurements := restoreMeasurements(phaseTimes, 0)
	require.Contains(t, measurements, metricRestoreTotalSeconds)
}
