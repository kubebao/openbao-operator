package bluegreen

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

func newPhaseMachineCluster() *openbaov1alpha1.OpenBaoCluster {
	cluster := newBlueGreenCluster()
	cluster.Spec.TLS = openbaov1alpha1.TLSConfig{
		Enabled:        true,
		RotationPeriod: "720h",
	}
	cluster.Spec.Storage = openbaov1alpha1.StorageConfig{
		Size: "10Gi",
	}
	cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{
		Image: "openbao/openbao-init:2.5.0",
	}
	cluster.Spec.Upgrade.BlueGreen = &openbaov1alpha1.BlueGreenConfig{
		AutoPromote: true,
	}
	cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseDeployingGreen
	cluster.Status.BlueGreen.GreenRevision = "green"
	return cluster
}

func mustValidationHookSpecHash(t *testing.T, hook *openbaov1alpha1.ValidationHookConfig) string {
	t.Helper()
	hash, err := validationHookSpecHash(hook)
	if err != nil {
		t.Fatalf("validationHookSpecHash() error = %v", err)
	}
	return hash
}

func newRevisionPod(cluster *openbaov1alpha1.OpenBaoCluster, revision, name string) *corev1.Pod {
	pod := newGreenPod(cluster, revision, name)
	pod.Status.Phase = corev1.PodRunning
	return pod
}

func markPodReadyUnsealed(pod *corev1.Pod) {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[portopenbao.LabelSealed] = "false"
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}}
}

func succeededExecutorJob(cluster *openbaov1alpha1.OpenBaoCluster, action upgrade.ExecutorAction) *batchv1.Job {
	return managedBlueGreenJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      upgrade.ExecutorJobName(cluster.Name, action, "", cluster.Status.BlueGreen.BlueRevision, cluster.Status.BlueGreen.GreenRevision),
			Namespace: cluster.Namespace,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
			Succeeded: 1,
		},
	}, cluster)
}

func succeededExecutorJobWithRunID(cluster *openbaov1alpha1.OpenBaoCluster, action upgrade.ExecutorAction, runID string) *batchv1.Job {
	return managedBlueGreenJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      upgrade.ExecutorJobName(cluster.Name, action, runID, cluster.Status.BlueGreen.BlueRevision, cluster.Status.BlueGreen.GreenRevision),
			Namespace: cluster.Namespace,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
			Succeeded: 1,
		},
	}, cluster)
}

func failedExecutorJobWithRunID(cluster *openbaov1alpha1.OpenBaoCluster, action upgrade.ExecutorAction, runID string) *batchv1.Job {
	return managedBlueGreenJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      upgrade.ExecutorJobName(cluster.Name, action, runID, cluster.Status.BlueGreen.BlueRevision, cluster.Status.BlueGreen.GreenRevision),
			Namespace: cluster.Namespace,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobFailed,
				Status: corev1.ConditionTrue,
			}},
			Failed: 1,
		},
	}, cluster)
}

type clusterOpsStub struct {
	podName string
	source  string
	ok      bool
}

const testBoolTrue = "true"

func (s *clusterOpsStub) FindLeaderPod(_ context.Context, _ logr.Logger, _ *openbaov1alpha1.OpenBaoCluster, _ []corev1.Pod) (string, string, bool) {
	return s.podName, s.source, s.ok
}

func TestHandlePhaseDeployingGreen_WaitsForBlueClusterReadiness(t *testing.T) {
	t.Parallel()

	t.Run("requeues when no blue pods exist", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, "")
		if err != nil {
			t.Fatalf("handlePhaseDeployingGreen() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseDeployingGreen() outcome = %+v, want short requeue", outcome)
		}
	})

	t.Run("requeues when blue revision labels are missing", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
		markPodReadyUnsealed(bluePod)
		delete(bluePod.Labels, constants.LabelOpenBaoRevision)

		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(bluePod).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, "")
		if err != nil {
			t.Fatalf("handlePhaseDeployingGreen() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseDeployingGreen() outcome = %+v, want short requeue", outcome)
		}
	})

	t.Run("requeues until a blue pod is ready and unsealed", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
		bluePod.Labels[portopenbao.LabelSealed] = testBoolTrue

		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(bluePod).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, "")
		if err != nil {
			t.Fatalf("handlePhaseDeployingGreen() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseDeployingGreen() outcome = %+v, want short requeue", outcome)
		}
	})
}

func TestHandlePhaseDeployingGreen_CreatesGreenStatefulSet(t *testing.T) {
	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
	markPodReadyUnsealed(bluePod)
	runtime := &workloadRuntimeStub{}
	manager := &Manager{
		client:          fake.NewClientBuilder().WithScheme(scheme).WithObjects(bluePod).Build(),
		scheme:          scheme,
		workloadRuntime: runtime,
	}

	outcome, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, "")
	if err != nil {
		t.Fatalf("handlePhaseDeployingGreen() error = %v", err)
	}
	if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
		t.Fatalf("handlePhaseDeployingGreen() outcome = %+v, want short requeue", outcome)
	}
	if !runtime.ensureStatefulSetCalled {
		t.Fatal("expected workload runtime to create the Green StatefulSet")
	}
	if runtime.lastRevision != cluster.Status.BlueGreen.GreenRevision {
		t.Fatalf("revision = %q, want %q", runtime.lastRevision, cluster.Status.BlueGreen.GreenRevision)
	}
	if runtime.lastVerifiedImage != cluster.Spec.Image {
		t.Fatalf("image = %q, want %q", runtime.lastVerifiedImage, cluster.Spec.Image)
	}
	if runtime.lastVerifiedInitImage != cluster.Spec.InitContainer.Image {
		t.Fatalf("init image = %q, want %q", runtime.lastVerifiedInitImage, cluster.Spec.InitContainer.Image)
	}
	if !runtime.lastDisableSelfInit {
		t.Fatal("expected Green StatefulSet to be created with self-init disabled")
	}
	if !strings.Contains(runtime.lastConfigContent, cluster.Status.BlueGreen.BlueRevision) {
		t.Fatalf("expected rendered config to reference blue revision %q", cluster.Status.BlueGreen.BlueRevision)
	}
}

func TestHandlePhaseDeployingGreen_TransitionsWhenGreenIsReady(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
	markPodReadyUnsealed(bluePod)

	replicas := cluster.Spec.Replicas
	greenStatefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-" + cluster.Status.BlueGreen.GreenRevision,
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: replicas,
		},
	}
	greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
	markPodReadyUnsealed(greenPod)

	manager := &Manager{
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(bluePod, greenStatefulSet, greenPod).
			Build(),
		scheme: scheme,
	}

	outcome, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, "")
	if err != nil {
		t.Fatalf("handlePhaseDeployingGreen() error = %v", err)
	}
	if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhaseJoiningMesh {
		t.Fatalf("handlePhaseDeployingGreen() outcome = %+v, want advance to JoiningMesh", outcome)
	}
}

func TestHandlePhaseDeployingGreen_FailsOnInvalidGreenSealLabel(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
	markPodReadyUnsealed(bluePod)

	replicas := cluster.Spec.Replicas
	greenStatefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-" + cluster.Status.BlueGreen.GreenRevision,
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: replicas,
		},
	}
	greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
	markPodReadyUnsealed(greenPod)
	greenPod.Labels[portopenbao.LabelSealed] = "invalid"

	manager := &Manager{
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(bluePod, greenStatefulSet, greenPod).
			Build(),
		scheme: scheme,
	}

	if _, err := manager.handlePhaseDeployingGreen(context.Background(), logr.Discard(), cluster, ""); err == nil {
		t.Fatal("expected invalid seal label to fail")
	}
}

func TestPhaseHandlers_ExecutorDrivenTransitions(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)

	tests := []struct {
		name      string
		phase     openbaov1alpha1.BlueGreenPhase
		action    upgrade.ExecutorAction
		call      func(*Manager, context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (phaseOutcome, error)
		nextPhase openbaov1alpha1.BlueGreenPhase
	}{
		{
			name:   "joining mesh advances to syncing",
			phase:  openbaov1alpha1.PhaseJoiningMesh,
			action: ActionJoinGreenNonVoters,
			call: func(m *Manager, ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (phaseOutcome, error) {
				return m.handlePhaseJoiningMesh(ctx, logger, cluster)
			},
			nextPhase: openbaov1alpha1.PhaseSyncing,
		},
		{
			name:   "promoting advances to demoting blue",
			phase:  openbaov1alpha1.PhasePromoting,
			action: ActionPromoteGreenVoters,
			call: func(m *Manager, ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (phaseOutcome, error) {
				return m.handlePhasePromoting(ctx, logger, cluster)
			},
			nextPhase: openbaov1alpha1.PhaseDemotingBlue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := newPhaseMachineCluster()
			cluster.Status.BlueGreen.Phase = tt.phase
			job := succeededExecutorJob(cluster, tt.action)
			manager := &Manager{
				client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build(),
				scheme: scheme,
			}

			outcome, err := tt.call(manager, context.Background(), logr.Discard(), cluster)
			if err != nil {
				t.Fatalf("phase handler error = %v", err)
			}
			if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != tt.nextPhase {
				t.Fatalf("phase handler outcome = %+v, want advance to %s", outcome, tt.nextPhase)
			}
		})
	}
}

func TestRecordBlueGreenUpgradeStartAssignsStableOperationID(t *testing.T) {
	t.Parallel()

	cluster := newPhaseMachineCluster()
	cluster.Status.BlueGreen.OperationID = ""
	manager := &Manager{}
	manager.recordBlueGreenUpgradeStart(logr.Discard(), cluster)
	firstOperationID := cluster.Status.BlueGreen.OperationID
	if firstOperationID == "" {
		t.Fatal("OperationID is empty after blue/green upgrade start")
	}

	manager.recordBlueGreenUpgradeStart(logr.Discard(), cluster)
	if cluster.Status.BlueGreen.OperationID != firstOperationID {
		t.Fatalf("OperationID changed from %q to %q", firstOperationID, cluster.Status.BlueGreen.OperationID)
	}
}

func TestHandlePhaseSyncing_Branches(t *testing.T) {
	t.Parallel()

	t.Run("snapshots manual promotion requirement when upgrade starts", func(t *testing.T) {
		t.Parallel()

		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle
		cluster.Status.BlueGreen.GreenRevision = ""
		cluster.Spec.Upgrade.BlueGreen.AutoPromote = false

		scheme := newBlueGreenTestScheme(t)
		blueSTS := executionTestStatefulSet(cluster, cluster.Status.BlueGreen.BlueRevision, cluster.Spec.Replicas, cluster.Status.BlueGreen.BlueImage)
		manager := &Manager{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(blueSTS).Build()}
		outcome, err := manager.handlePhaseIdle(context.Background(), logr.Discard(), cluster, "")
		if err != nil {
			t.Fatalf("handlePhaseIdle() error = %v", err)
		}
		if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhaseDeployingGreen {
			t.Fatalf("handlePhaseIdle() outcome = %+v, want advance to DeployingGreen", outcome)
		}
		if !cluster.Status.BlueGreen.ManualPromotionRequired {
			t.Fatal("ManualPromotionRequired = false, want true")
		}
	})

	t.Run("waits for minimum sync duration", func(t *testing.T) {
		t.Parallel()

		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.StartTime = &metav1.Time{Time: time.Now()}
		cluster.Spec.Upgrade.BlueGreen.Verification = &openbaov1alpha1.VerificationConfig{
			MinSyncDuration: "10m",
		}

		manager := &Manager{}
		outcome, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseSyncing() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after <= 0 {
			t.Fatalf("handlePhaseSyncing() outcome = %+v, want positive requeue", outcome)
		}
	})

	t.Run("rejects nil start time when minimum sync duration is configured", func(t *testing.T) {
		t.Parallel()

		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.StartTime = nil
		cluster.Spec.Upgrade.BlueGreen.Verification = &openbaov1alpha1.VerificationConfig{
			MinSyncDuration: "10m",
		}

		manager := &Manager{}
		if _, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster); err == nil {
			t.Fatal("expected nil start time to fail")
		}
	})

	t.Run("rejects invalid minimum sync duration", func(t *testing.T) {
		t.Parallel()

		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.StartTime = &metav1.Time{Time: time.Now().Add(-time.Minute)}
		cluster.Spec.Upgrade.BlueGreen.Verification = &openbaov1alpha1.VerificationConfig{
			MinSyncDuration: "definitely-not-a-duration",
		}

		manager := &Manager{}
		if _, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster); err == nil {
			t.Fatal("expected invalid min sync duration to fail")
		}
	})

	t.Run("holds when current upgrade requires manual approval even if auto promote is later enabled", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.ManualPromotionRequired = true
		cluster.Spec.Upgrade.BlueGreen.AutoPromote = true
		job := succeededExecutorJob(cluster, ActionWaitGreenSynced)
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseSyncing() error = %v", err)
		}
		if outcome.kind != phaseOutcomeHold {
			t.Fatalf("handlePhaseSyncing() outcome = %+v, want hold", outcome)
		}
	})

	t.Run("advances to promoting when current upgrade was snapshotted for auto promotion", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.ManualPromotionRequired = false
		cluster.Spec.Upgrade.BlueGreen.AutoPromote = false
		job := succeededExecutorJob(cluster, ActionWaitGreenSynced)
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseSyncing() error = %v", err)
		}
		if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhasePromoting {
			t.Fatalf("handlePhaseSyncing() outcome = %+v, want advance to Promoting", outcome)
		}
	})

	t.Run("advances to promoting after sync completes when promote request is set", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.ManualPromotionRequired = true
		cluster.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{
			Promote: "2026-03-10T12:00:00Z",
		}
		job := succeededExecutorJob(cluster, ActionWaitGreenSynced)
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseSyncing(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseSyncing() error = %v", err)
		}
		if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhasePromoting {
			t.Fatalf("handlePhaseSyncing() outcome = %+v, want advance to Promoting", outcome)
		}
		if outcome.acknowledgements.Promote != "2026-03-10T12:00:00Z" {
			t.Fatalf("promote acknowledgement = %q, want request token", outcome.acknowledgements.Promote)
		}
	})

	t.Run("aborts before promotion when pre-promotion hook fails in an early phase", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    core.UpgradeOperationLockHolder,
			Message:   "blue/green upgrade phase Syncing",
		}
		cluster.Spec.Upgrade.BlueGreen.AutoRollback = &openbaov1alpha1.AutoRollbackConfig{
			Enabled:             true,
			OnValidationFailure: true,
		}
		cluster.Spec.Upgrade.BlueGreen.Verification = &openbaov1alpha1.VerificationConfig{
			PrePromotionHook: &openbaov1alpha1.ValidationHookConfig{
				Image:   "openbao/openbao:2.5.0",
				Command: []string{"/bin/sh", "-ec"},
				Args:    []string{"exit 1"},
			},
		}
		cluster.Status.BlueGreen.OperationID = "operation-1"
		hookSpecHash := mustValidationHookSpecHash(t, cluster.Spec.Upgrade.BlueGreen.Verification.PrePromotionHook)
		cluster.Status.BlueGreen.ValidationHook = &openbaov1alpha1.BlueGreenValidationHookStatus{
			OperationID:    cluster.Status.BlueGreen.OperationID,
			GreenRevision:  cluster.Status.BlueGreen.GreenRevision,
			SpecHash:       hookSpecHash,
			Stage:          openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved,
			JobName:        validationHookJobName(cluster.Name, cluster.Status.BlueGreen.OperationID, cluster.Status.BlueGreen.GreenRevision, hookSpecHash),
			TerminalResult: openbaov1alpha1.BlueGreenValidationHookResultFailed,
		}
		blueRevision := cluster.Status.BlueGreen.BlueRevision
		greenRevision := cluster.Status.BlueGreen.GreenRevision
		waitSyncedJob := succeededExecutorJob(cluster, ActionWaitGreenSynced)
		greenStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithObjects(cluster, waitSyncedJob, greenStatefulSet).
			Build()
		manager := &Manager{client: client, reader: client, scheme: scheme}

		result, err := manager.executeStateMachine(context.Background(), logr.Discard(), cluster, "", &upgrade.RequestAcknowledgements{})
		if err != nil {
			t.Fatalf("executeStateMachine() error = %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Fatalf("executeStateMachine() result = %+v, want terminal abort without requeue", result)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
			t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
		}
		if cluster.Status.BlueGreen.GreenRevision != "" {
			t.Fatalf("green revision = %q, want empty after abort", cluster.Status.BlueGreen.GreenRevision)
		}
		if cluster.Status.BlueGreen.RollbackStartTime != nil {
			t.Fatal("rollback start time set; early pre-promotion hook failure should abort before cutover")
		}
		if cluster.Status.OperationLock != nil {
			t.Fatalf("operation lock = %+v, want released", cluster.Status.OperationLock)
		}

		staleGreen := &appsv1.StatefulSet{}
		err = client.Get(context.Background(), types.NamespacedName{
			Name:      greenStatefulSet.Name,
			Namespace: greenStatefulSet.Namespace,
		}, staleGreen)
		if err == nil {
			t.Fatal("green StatefulSet still exists after pre-promotion abort")
		}
		if !apierrors.IsNotFound(err) {
			t.Fatalf("get green StatefulSet after abort: %v", err)
		}

		promoteJobName := upgrade.ExecutorJobName(
			cluster.Name,
			ActionPromoteGreenVoters,
			"",
			blueRevision,
			greenRevision,
		)
		promoteJob := &batchv1.Job{}
		err = client.Get(context.Background(), types.NamespacedName{
			Name:      promoteJobName,
			Namespace: cluster.Namespace,
		}, promoteJob)
		if err == nil {
			t.Fatalf("promotion job %q exists after validation hook failure", promoteJobName)
		}
		if !apierrors.IsNotFound(err) {
			t.Fatalf("get promotion job after abort: %v", err)
		}
	})
}

func TestHandlePhaseDemotingBlue_Branches(t *testing.T) {
	t.Parallel()

	t.Run("requeues until green pods are ready and unsealed", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseDemotingBlue
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(greenPod).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseDemotingBlue(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseDemotingBlue() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseDemotingBlue() outcome = %+v, want short requeue", outcome)
		}
	})

	t.Run("advances to cleanup once demotion succeeds and green leads", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseDemotingBlue
		cluster.Spec.Replicas = 1
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		markPodReadyUnsealed(greenPod)
		job := succeededExecutorJob(cluster, ActionDemoteBlueNonVotersStepDown)
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(greenPod, job).Build(),
			scheme: scheme,
			clusterOps: &clusterOpsStub{
				podName: greenPod.Name,
				source:  "label",
				ok:      true,
			},
		}

		outcome, err := manager.handlePhaseDemotingBlue(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseDemotingBlue() error = %v", err)
		}
		if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhaseCleanup {
			t.Fatalf("handlePhaseDemotingBlue() outcome = %+v, want advance to Cleanup", outcome)
		}
		state, ok := core.GetUpgradeMetricsSession(cluster.Namespace, cluster.Name)
		if !ok || !state.StepDownCounted {
			t.Fatal("expected step-down metrics state to be recorded")
		}
		core.DeleteUpgradeMetricsSession(cluster.Namespace, cluster.Name)
	})
}

func TestHandlePhaseCleanup_Branches(t *testing.T) {
	t.Parallel()

	t.Run("requeues until cleanup preconditions are satisfied", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		manager := &Manager{
			client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(greenPod).Build(),
			scheme:     scheme,
			clusterOps: &clusterOpsStub{},
		}

		outcome, err := manager.handlePhaseCleanup(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseCleanup() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseCleanup() outcome = %+v, want short requeue", outcome)
		}
	})

	t.Run("deletes blue statefulset and requeues", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
		cluster.Spec.Replicas = 1
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		markPodReadyUnsealed(greenPod)
		greenPod.Labels[portopenbao.LabelActive] = testBoolTrue
		job := succeededExecutorJob(cluster, ActionRemoveBluePeers)
		blueStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.BlueRevision)
		manager := &Manager{
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(greenPod, job, blueStatefulSet).Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseCleanup(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseCleanup() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseCleanup() outcome = %+v, want short requeue", outcome)
		}
	})

	t.Run("finalizes once blue resources are gone", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
		cluster.Spec.Replicas = 1
		expectedBlueRevision := cluster.Status.BlueGreen.GreenRevision
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    core.UpgradeOperationLockHolder,
			Message:   "blue/green upgrade phase Cleanup",
		}
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		markPodReadyUnsealed(greenPod)
		greenPod.Labels[portopenbao.LabelActive] = testBoolTrue
		job := succeededExecutorJob(cluster, ActionRemoveBluePeers)
		manager := &Manager{
			client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(cluster, greenPod, job).
				Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseCleanup(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseCleanup() error = %v", err)
		}
		if outcome.kind != phaseOutcomeRequeueAfter || outcome.after != constants.RequeueShort {
			t.Fatalf("handlePhaseCleanup() outcome = %+v, want short requeue", outcome)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
			t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
		}
		if cluster.Status.BlueGreen.BlueRevision != expectedBlueRevision {
			t.Fatalf("blue revision = %q, want %q", cluster.Status.BlueGreen.BlueRevision, expectedBlueRevision)
		}
		if cluster.Status.OperationLock != nil {
			t.Fatal("expected operation lock to be released")
		}
	})

	t.Run("transitions to read replica restore before idle when steady reads are configured", func(t *testing.T) {
		t.Parallel()

		scheme := newBlueGreenTestScheme(t)
		cluster := newPhaseMachineCluster()
		cluster.Spec.Replicas = 1
		cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{Replicas: 2}
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    core.UpgradeOperationLockHolder,
			Message:   "blue/green upgrade phase Cleanup",
		}
		greenPod := newRevisionPod(cluster, cluster.Status.BlueGreen.GreenRevision, "green-0")
		markPodReadyUnsealed(greenPod)
		greenPod.Labels[portopenbao.LabelActive] = testBoolTrue
		job := succeededExecutorJob(cluster, ActionRemoveBluePeers)
		manager := &Manager{
			client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(cluster, greenPod, job).
				Build(),
			scheme: scheme,
		}

		outcome, err := manager.handlePhaseCleanup(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePhaseCleanup() error = %v", err)
		}
		if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhaseRestoringReadReplicas {
			t.Fatalf("handlePhaseCleanup() outcome = %+v, want advance to read replica restore", outcome)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseCleanup {
			t.Fatal("phase handler advanced before its outcome was applied")
		}
		result, err := manager.applyOutcome(t.Context(), logr.Discard(), cluster, outcome)
		if err != nil || result.RequeueAfter != constants.RequeueShort {
			t.Fatalf("applyOutcome() = %+v, %v, want short requeue", result, err)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseRestoringReadReplicas {
			t.Fatalf("phase = %s, want %s", cluster.Status.BlueGreen.Phase, openbaov1alpha1.PhaseRestoringReadReplicas)
		}
		if cluster.Status.BlueGreen.BlueRevision == "" {
			t.Fatal("expected active revision to be promoted before read replica restore")
		}
		if cluster.Status.BlueGreen.GreenRevision != "" {
			t.Fatalf("green revision = %q, want empty", cluster.Status.BlueGreen.GreenRevision)
		}
		if cluster.Status.OperationLock == nil {
			t.Fatal("expected operation lock to stay held until steady reads are restored")
		}
	})
}

func TestHandlePhaseRollingBack_AdvancesWhenConsensusIsRepaired(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseRollingBack
	bluePod := newRevisionPod(cluster, cluster.Status.BlueGreen.BlueRevision, "blue-0")
	job := succeededExecutorJobWithRunID(cluster, ActionRepairConsensus, rollbackRunID(cluster))
	manager := &Manager{
		client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(bluePod, job).Build(),
		scheme: scheme,
		clusterOps: &clusterOpsStub{
			podName: bluePod.Name,
			source:  "label",
			ok:      true,
		},
	}

	outcome, err := manager.handlePhaseRollingBack(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("handlePhaseRollingBack() error = %v", err)
	}
	if outcome.kind != phaseOutcomeAdvance || outcome.nextPhase != openbaov1alpha1.PhaseRollbackCleanup {
		t.Fatalf("handlePhaseRollingBack() outcome = %+v, want advance to RollbackCleanup", outcome)
	}
}

func TestHandlePhaseRollbackCleanup_FinalizesRollback(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseRollbackCleanup
	cluster.Status.BlueGreen.RollbackReason = "cleanup failed"
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
		Operation: openbaov1alpha1.ClusterOperationUpgrade,
		Holder:    core.UpgradeOperationLockHolder,
		Message:   "blue/green upgrade phase RollbackCleanup",
	}
	job := succeededExecutorJobWithRunID(cluster, ActionRemoveGreenPeers, rollbackRunID(cluster))
	greenStatefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-" + cluster.Status.BlueGreen.GreenRevision,
			Namespace: cluster.Namespace,
		},
	}
	greenStatefulSet.OwnerReferences = job.OwnerReferences
	greenStatefulSet.Annotations = job.Annotations
	greenStatefulSet.Spec.Template.Labels = map[string]string{constants.LabelOpenBaoRevision: "green"}
	manager := &Manager{
		clusterOps: &clusterOpsStub{ok: true, podName: "example-blue-0"},
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithObjects(cluster, job, greenStatefulSet).
			Build(),
		scheme: scheme,
	}

	outcome, err := manager.handlePhaseRollbackCleanup(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("handlePhaseRollbackCleanup() error = %v", err)
	}
	if outcome.kind != phaseOutcomeRequeueAfter {
		t.Fatalf("expected StatefulSet deletion to requeue, got %+v", outcome)
	}
	outcome, err = manager.handlePhaseRollbackCleanup(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.kind != phaseOutcomeDone {
		t.Fatalf("handlePhaseRollbackCleanup() outcome = %+v, want done", outcome)
	}
	if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
		t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
	}
	if cluster.Status.OperationLock != nil {
		t.Fatal("expected operation lock to be released")
	}

	// The desired target still differs from CurrentVersion. Reconciliation starts
	// another operation without requiring a rolling-upgrade retry request.
	blueSTS := executionTestStatefulSet(cluster, cluster.Status.BlueGreen.BlueRevision, cluster.Spec.Replicas, cluster.Status.BlueGreen.BlueImage)
	if err := manager.client.Create(context.Background(), blueSTS); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() after rollback: %v", err)
	}
	if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseDeployingGreen {
		t.Fatalf("phase after rollback reconcile = %s, want DeployingGreen", cluster.Status.BlueGreen.Phase)
	}
	if cluster.Status.BlueGreen.OperationID == "" {
		t.Fatal("operation identity must be assigned before executor phases")
	}
	jobs := &batchv1.JobList{}
	if err := manager.client.List(context.Background(), jobs); err != nil {
		t.Fatalf("List Jobs: %v", err)
	}
	if len(jobs.Items) != 1 || jobs.Items[0].Name != job.Name {
		t.Fatal("starting an operation must not create an executor Job before status is persisted")
	}

}

func TestHandlePhaseRollbackCleanup_EntersBreakGlassWhenPeerRemovalFails(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	cluster := newPhaseMachineCluster()
	cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseRollbackCleanup
	job := failedExecutorJobWithRunID(cluster, ActionRemoveGreenPeers, rollbackRunID(cluster))

	manager := &Manager{
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cluster, job).
			Build(),
		scheme: scheme,
	}

	outcome, err := manager.handlePhaseRollbackCleanup(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("handlePhaseRollbackCleanup() error = %v", err)
	}
	if outcome.kind != phaseOutcomeHold {
		t.Fatalf("handlePhaseRollbackCleanup() outcome = %+v, want hold", outcome)
	}
	if cluster.Status.BreakGlass == nil || !cluster.Status.BreakGlass.Active {
		t.Fatal("expected break glass to be active")
	}
	if cluster.Status.BreakGlass.Reason != openbaov1alpha1.BreakGlassReasonRollbackCleanupPeerRemovalFailed {
		t.Fatalf("break glass reason = %q, want %q", cluster.Status.BreakGlass.Reason, openbaov1alpha1.BreakGlassReasonRollbackCleanupPeerRemovalFailed)
	}
}
