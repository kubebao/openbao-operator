package bluegreen

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	upgradecore "github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func newBlueGreenTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbao scheme: %v", err)
	}
	return scheme
}

func newBlueGreenCluster() *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
			UID:       types.UID("example-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Image:    "openbao/openbao:2.5.0",
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized:    true,
			CurrentVersion: "2.4.4",
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseSyncing,
				BlueRevision: "blue",
			},
		},
	}
}

func newGreenPod(cluster *openbaov1alpha1.OpenBaoCluster, revision, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppInstance:     cluster.Name,
				constants.LabelAppName:         constants.LabelValueAppNameOpenBao,
				constants.LabelOpenBaoRevision: revision,
			},
		},
	}
}

func TestCheckAbortConditions_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("returns false for idle or missing green revision", func(t *testing.T) {
		t.Parallel()

		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle
		manager := &Manager{}

		shouldAbort, err := manager.checkAbortConditions(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("checkAbortConditions() error = %v", err)
		}
		if shouldAbort {
			t.Fatal("checkAbortConditions() = true, want false")
		}

		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.GreenRevision = ""
		shouldAbort, err = manager.checkAbortConditions(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("checkAbortConditions() error = %v", err)
		}
		if shouldAbort {
			t.Fatal("checkAbortConditions() = true with empty green revision, want false")
		}
	})

	t.Run("detects unhealthy green pods", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name   string
			mutate func(*corev1.Pod)
		}{
			{
				name: "crashloopbackoff",
				mutate: func(pod *corev1.Pod) {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
					}}
				},
			},
			{
				name: "imagepullbackoff",
				mutate: func(pod *corev1.Pod) {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
						},
					}}
				},
			},
			{
				name: "terminated nonzero",
				mutate: func(pod *corev1.Pod) {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{ExitCode: 2},
						},
					}}
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cluster := newBlueGreenCluster()
				cluster.Status.BlueGreen.GreenRevision = deploymentNameSuffix
				pod := newGreenPod(cluster, deploymentNameSuffix, deploymentNameSuffix+"-0")
				tt.mutate(pod)

				scheme := newBlueGreenTestScheme(t)
				client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
				manager := &Manager{client: client, scheme: scheme}

				shouldAbort, err := manager.checkAbortConditions(context.Background(), logr.Discard(), cluster)
				if err != nil {
					t.Fatalf("checkAbortConditions() error = %v", err)
				}
				if !shouldAbort {
					t.Fatal("checkAbortConditions() = false, want true")
				}
			})
		}
	})

	t.Run("healthy green pods do not abort", func(t *testing.T) {
		t.Parallel()

		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.GreenRevision = deploymentNameSuffix
		pod := newGreenPod(cluster, deploymentNameSuffix, deploymentNameSuffix+"-0")
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
		}}

		scheme := newBlueGreenTestScheme(t)
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
		manager := &Manager{client: client, scheme: scheme}

		shouldAbort, err := manager.checkAbortConditions(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("checkAbortConditions() error = %v", err)
		}
		if shouldAbort {
			t.Fatal("checkAbortConditions() = true, want false")
		}
	})
}

func TestMaybeAbortUpgrade_OnlyDeletesGreenBeforePromotion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase     openbaov1alpha1.BlueGreenPhase
		wantAbort bool
	}{
		{phase: openbaov1alpha1.PhaseDeployingGreen, wantAbort: true},
		{phase: openbaov1alpha1.PhaseJoiningMesh, wantAbort: true},
		{phase: openbaov1alpha1.PhaseSyncing, wantAbort: true},
		{phase: openbaov1alpha1.PhasePromoting},
		{phase: openbaov1alpha1.PhaseDemotingBlue},
		{phase: openbaov1alpha1.PhaseCleanup},
		{phase: openbaov1alpha1.PhaseRestoringReadReplicas},
		{phase: openbaov1alpha1.PhaseRollingBack},
		{phase: openbaov1alpha1.PhaseRollbackCleanup},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			scheme := newBlueGreenTestScheme(t)
			cluster := newBlueGreenCluster()
			cluster.Status.BlueGreen.Phase = tt.phase
			cluster.Status.BlueGreen.GreenRevision = deploymentNameSuffix

			pod := newGreenPod(cluster, deploymentNameSuffix, deploymentNameSuffix+"-0")
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 137},
				},
			}}
			greenStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(cluster, pod, greenStatefulSet).
				Build()

			manager := &Manager{client: client, scheme: scheme}
			handled, _, err := manager.maybeAbortUpgrade(context.Background(), logr.Discard(), cluster)
			if err != nil {
				t.Fatalf("maybeAbortUpgrade() error = %v", err)
			}
			if handled != tt.wantAbort {
				t.Fatalf("maybeAbortUpgrade() handled = %t, want %t", handled, tt.wantAbort)
			}

			greenErr := client.Get(context.Background(), types.NamespacedName{Name: greenStatefulSet.Name, Namespace: greenStatefulSet.Namespace}, &appsv1.StatefulSet{})
			if tt.wantAbort {
				if greenErr == nil {
					t.Fatal("green StatefulSet still exists after early-phase abort")
				}
				return
			}
			if greenErr != nil {
				t.Fatalf("green StatefulSet deleted in phase %s: %v", tt.phase, greenErr)
			}
			if cluster.Status.BlueGreen.Phase != tt.phase {
				t.Fatalf("phase = %s, want %s", cluster.Status.BlueGreen.Phase, tt.phase)
			}
		})
	}
}

func TestMaybeAbortUpgrade_CleansUpGreenAndReleasesLock(t *testing.T) {
	scheme := newBlueGreenTestScheme(t)
	cluster := newBlueGreenCluster()
	cluster.Status.BlueGreen.GreenRevision = deploymentNameSuffix
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationUpgrade,
		Holder:    upgradecore.UpgradeOperationLockHolder,
		Message:   "blue/green upgrade phase Syncing",
	}

	pod := newGreenPod(cluster, deploymentNameSuffix, deploymentNameSuffix+"-0")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	greenStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster, pod, greenStatefulSet).
		Build()

	manager := &Manager{client: client, scheme: scheme}
	handled, result, err := manager.maybeAbortUpgrade(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("maybeAbortUpgrade() error = %v", err)
	}
	if !handled {
		t.Fatal("maybeAbortUpgrade() handled = false, want true")
	}
	if result.RequeueAfter != constants.RequeueShort {
		t.Fatalf("maybeAbortUpgrade() requeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
	}
	if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
		t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
	}
	if cluster.Status.BlueGreen.GreenRevision != "" {
		t.Fatalf("green revision = %q, want empty", cluster.Status.BlueGreen.GreenRevision)
	}
	if cluster.Status.OperationLock != nil {
		t.Fatal("operation lock still held after abort")
	}

	staleGreen := &appsv1.StatefulSet{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: greenStatefulSet.Name, Namespace: greenStatefulSet.Namespace}, staleGreen); err == nil {
		t.Fatal("green StatefulSet still exists after abort")
	}
}

func TestFinalizeUpgradeTerminalStatePromotesGreenToBlue(t *testing.T) {
	scheme := newBlueGreenTestScheme(t)
	cluster := newBlueGreenCluster()
	start := metav1.NewTime(time.Unix(1700000000, 0))
	cluster.Status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{
		Phase:           openbaov1alpha1.PhaseCleanup,
		BlueRevision:    "blue",
		GreenRevision:   "green",
		StartTime:       &start,
		JobFailureCount: 3,
		LastJobFailure:  "boom",
	}
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationUpgrade,
		Holder:    upgradecore.UpgradeOperationLockHolder,
		Message:   "blue/green upgrade phase Cleanup",
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster).
		Build()
	manager := &Manager{client: client, scheme: scheme}

	upgradecore.CaptureBlueGreenTarget(cluster)
	if err := manager.finalizeUpgradeTerminalState(context.Background(), logr.Discard(), cluster, true); err != nil {
		t.Fatalf("finalizeUpgradeTerminalState() error = %v", err)
	}
	if cluster.Status.BlueGreen.BlueRevision != deploymentNameSuffix {
		t.Fatalf("blue revision = %q, want green", cluster.Status.BlueGreen.BlueRevision)
	}
	if cluster.Status.BlueGreen.BlueImage != cluster.Spec.Image {
		t.Fatalf("blue image = %q, want %q", cluster.Status.BlueGreen.BlueImage, cluster.Spec.Image)
	}
	if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
		t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
	}
	if cluster.Status.BlueGreen.GreenRevision != "" {
		t.Fatalf("green revision = %q, want empty", cluster.Status.BlueGreen.GreenRevision)
	}
	if cluster.Status.BlueGreen.StartTime != nil {
		t.Fatal("start time not cleared")
	}
	if cluster.Status.BlueGreen.JobFailureCount != 0 {
		t.Fatalf("job failure count = %d, want 0", cluster.Status.BlueGreen.JobFailureCount)
	}
	if cluster.Status.BlueGreen.LastJobFailure != "" {
		t.Fatalf("last job failure = %q, want empty", cluster.Status.BlueGreen.LastJobFailure)
	}
	if cluster.Status.OperationLock != nil {
		t.Fatal("operation lock still held after finalize")
	}
}

func TestTriggerRollbackOrAbort_EarlyAndLatePhase(t *testing.T) {
	scheme := newBlueGreenTestScheme(t)

	t.Run("early phase aborts upgrade", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.GreenRevision = "green"
		greenStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cluster, greenStatefulSet).
			Build()

		manager := &Manager{client: client, scheme: scheme}
		result, err := manager.triggerRollbackOrAbort(context.Background(), logr.Discard(), cluster, "sync failed")
		if err != nil {
			t.Fatalf("triggerRollbackOrAbort() error = %v", err)
		}
		if result != (recon.Result{}) {
			t.Fatalf("triggerRollbackOrAbort() result = %v, want zero", result)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
			t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
		}
	})

	t.Run("late phase triggers rollback", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseDemotingBlue
		phaseStart := metav1.NewTime(time.Now().Add(-time.Minute))
		cluster.Status.BlueGreen.StartTime = &phaseStart
		cluster.Status.BlueGreen.JobFailureCount = 3
		cluster.Status.BlueGreen.LastJobFailure = "promotion-job"

		manager := &Manager{}

		result, err := manager.triggerRollbackOrAbort(context.Background(), logr.Discard(), cluster, "cleanup failed")
		if err != nil {
			t.Fatalf("triggerRollbackOrAbort() error = %v", err)
		}
		if result.RequeueAfter != constants.RequeueShort {
			t.Fatalf("requeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueShort)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseRollingBack {
			t.Fatalf("phase = %s, want RollingBack", cluster.Status.BlueGreen.Phase)
		}
		if cluster.Status.BlueGreen.RollbackReason != "cleanup failed" {
			t.Fatalf("rollback reason = %q, want %q", cluster.Status.BlueGreen.RollbackReason, "cleanup failed")
		}
		if cluster.Status.BlueGreen.RollbackStartTime == nil {
			t.Fatal("rollback start time not set")
		}
		if !cluster.Status.BlueGreen.StartTime.Equal(&phaseStart) || cluster.Status.BlueGreen.JobFailureCount != 3 || cluster.Status.BlueGreen.LastJobFailure != "promotion-job" {
			t.Fatal("starting rollback must preserve the phase timer and failure history")
		}

	})

	for _, phase := range []openbaov1alpha1.BlueGreenPhase{
		openbaov1alpha1.PhaseCleanup,
		openbaov1alpha1.PhaseRestoringReadReplicas,
	} {
		t.Run("refuses rollback in "+string(phase), func(t *testing.T) {
			cluster := newBlueGreenCluster()
			cluster.Status.BlueGreen.Phase = phase
			cluster.Status.BlueGreen.GreenRevision = deploymentNameSuffix
			cluster.Status.BlueGreen.JobFailureCount = 5
			recorder := events.NewFakeRecorder(10)
			manager := &Manager{recorder: recorder}

			result, err := manager.triggerRollbackOrAbort(context.Background(), logr.Discard(), cluster, "cleanup peer removal job failure threshold exceeded")
			if err != nil {
				t.Fatalf("triggerRollbackOrAbort() error = %v", err)
			}
			if result.RequeueAfter != constants.RequeueStandard {
				t.Fatalf("requeueAfter = %v, want %v", result.RequeueAfter, constants.RequeueStandard)
			}
			if cluster.Status.BlueGreen.Phase != phase {
				t.Fatalf("phase = %s, want %s", cluster.Status.BlueGreen.Phase, phase)
			}
			if cluster.Status.BlueGreen.RollbackStartTime != nil {
				t.Fatal("rollback started after Blue peer removal")
			}
			if cluster.Status.BlueGreen.JobFailureCount != 5 {
				t.Fatalf("job failure count = %d, want 5 so the next attempt uses a new run ID", cluster.Status.BlueGreen.JobFailureCount)
			}
			expectEventContains(t, recorder, "Warning", ReasonRollbackRefused)
		})
	}
}
