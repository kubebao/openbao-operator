package bluegreen

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

const staleGreenRevision = "green-old"

func TestManager_ShouldReconcileBlueGreen(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	tests := []struct {
		name    string
		cluster *openbaov1alpha1.OpenBaoCluster
		want    bool
	}{
		{
			name:    "nil upgrade config",
			cluster: &openbaov1alpha1.OpenBaoCluster{},
			want:    false,
		},
		{
			name: "rolling strategy",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyRollingUpdate},
				},
			},
			want: false,
		},
		{
			name: "bluegreen strategy",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyBlueGreen},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mgr.shouldReconcileBlueGreen(logr.Discard(), tt.cluster); got != tt.want {
				t.Fatalf("shouldReconcileBlueGreen()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestManager_MaybeAcquireUpgradeLock_Contention(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lock-contention",
			Namespace: "default",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{Phase: openbaov1alpha1.PhaseDeployingGreen},
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationBackup,
				Holder:    "backup-controller",
				Message:   "backup running",
			},
		},
	}
	scheme := newBlueGreenTestScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster).
		Build()
	mgr := &Manager{client: k8sClient, reader: k8sClient, scheme: scheme}

	t.Run("not active upgrade requeues on contention", func(t *testing.T) {
		handled, result, err := mgr.maybeAcquireUpgradeLock(context.Background(), logr.Discard(), cluster.DeepCopy(), false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatalf("expected handled=true")
		}
		if result.RequeueAfter != opslifecycle.RequeueDelay(opslifecycle.RetryClassLockContention) {
			t.Fatalf("requeueAfter=%v, want %v", result.RequeueAfter, opslifecycle.RequeueDelay(opslifecycle.RetryClassLockContention))
		}
	})

	t.Run("active upgrade returns explicit error on contention", func(t *testing.T) {
		handled, result, err := mgr.maybeAcquireUpgradeLock(context.Background(), logr.Discard(), cluster.DeepCopy(), true, true)
		if !handled {
			t.Fatalf("expected handled=true")
		}
		if result != (recon.Result{}) {
			t.Fatalf("result=%v, want zero", result)
		}
		if err == nil || !strings.Contains(err.Error(), "operation lock is held") {
			t.Fatalf("expected contention error, got %v", err)
		}
	})
}

func TestManager_MaybeAcquireUpgradeLock_SuccessAndNoop(t *testing.T) {
	t.Parallel()

	t.Run("skips when no upgrade is active or needed", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		mgr := &Manager{}

		handled, result, err := mgr.maybeAcquireUpgradeLock(context.Background(), logr.Discard(), cluster, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Fatalf("expected handled=false")
		}
		if result != (recon.Result{}) {
			t.Fatalf("result=%v, want zero", result)
		}
	})

	t.Run("acquires lock when upgrade needs to proceed", func(t *testing.T) {
		scheme := newBlueGreenTestScheme(t)
		cluster := newBlueGreenCluster()
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithObjects(cluster).
			Build()
		mgr := &Manager{client: client, scheme: scheme}

		handled, result, err := mgr.maybeAcquireUpgradeLock(context.Background(), logr.Discard(), cluster, false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Fatalf("expected handled=false")
		}
		if result != (recon.Result{}) {
			t.Fatalf("result=%v, want zero", result)
		}
		if !core.IsUpgradeOperationLockHeldByUs(cluster.Status.OperationLock) {
			t.Fatalf("expected upgrade lock to be held by the blue/green manager, got %+v", cluster.Status.OperationLock)
		}
	})
}

func TestManager_HandleNoUpgradeNeeded(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}

	t.Run("current version not established", func(t *testing.T) {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			Spec: openbaov1alpha1.OpenBaoClusterSpec{Version: "1.2.3"},
			Status: openbaov1alpha1.OpenBaoClusterStatus{
				CurrentVersion: "",
			},
		}
		handled, result, err := mgr.handleNoUpgradeNeeded(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatalf("expected handled=true")
		}
		if result.RequeueAfter != 1*time.Minute {
			t.Fatalf("requeueAfter=%v, want 1m", result.RequeueAfter)
		}
	})

	t.Run("already on target version", func(t *testing.T) {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			Spec: openbaov1alpha1.OpenBaoClusterSpec{Version: "1.2.3"},
			Status: openbaov1alpha1.OpenBaoClusterStatus{
				CurrentVersion: "1.2.3",
			},
		}
		handled, result, err := mgr.handleNoUpgradeNeeded(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatalf("expected handled=true")
		}
		if result != (recon.Result{}) {
			t.Fatalf("result=%v, want zero", result)
		}
	})

	t.Run("upgrade still needed", func(t *testing.T) {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			Spec: openbaov1alpha1.OpenBaoClusterSpec{Version: "1.2.4"},
			Status: openbaov1alpha1.OpenBaoClusterStatus{
				CurrentVersion: "1.2.3",
			},
		}
		handled, result, err := mgr.handleNoUpgradeNeeded(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Fatalf("expected handled=false")
		}
		if result != (recon.Result{}) {
			t.Fatalf("result=%v, want zero", result)
		}
	})
}

func TestManager_ReconcileBlueGreen_BlocksUnsupportedTargetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		current       string
		target        string
		image         string
		wantErrSubstr string
		wantReason    string
	}{
		{
			name:          "invalid target version",
			current:       "2.4.4",
			target:        "latest",
			wantErrSubstr: "invalid target version",
			wantReason:    upgrade.ReasonInvalidVersion,
		},
		{
			name:          "downgrade target version",
			current:       "2.5.0",
			target:        "2.4.4",
			wantErrSubstr: "downgrade from 2.5.0 to 2.4.4 is not supported",
			wantReason:    upgrade.ReasonDowngradeBlocked,
		},
		{
			name:          "semver image tag mismatch",
			current:       "2.4.4",
			target:        "2.5.0",
			image:         "openbao/openbao:2.4.4",
			wantErrSubstr: "spec.image tag \"2.4.4\" does not match spec.version \"2.5.0\"",
			wantReason:    upgrade.ReasonImageVersionMismatch,
		},
		{
			name:          "OpenBao 2.6.0 mixed-version blue green incompatibility",
			current:       "2.5.5",
			target:        "2.6.0",
			wantErrSubstr: "blue/green upgrade from OpenBao 2.5.5 to 2.6.0 is blocked",
			wantReason:    upgrade.ReasonBlueGreenVersionIncompatible,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newBlueGreenTestScheme(t)
			cluster := newBlueGreenCluster()
			cluster.Spec.Version = tt.target
			cluster.Spec.Image = tt.image
			if cluster.Spec.Image == "" {
				cluster.Spec.Image = "openbao/openbao:" + tt.target
			}
			cluster.Status.CurrentVersion = tt.current
			cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(cluster).
				Build()
			mgr := &Manager{client: client, scheme: scheme}

			_, err := mgr.reconcileBlueGreen(context.Background(), logr.Discard(), cluster, "", &upgrade.RequestAcknowledgements{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("reconcileBlueGreen() error = %v, want contains %q", err, tt.wantErrSubstr)
			}
			if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
				t.Fatalf("expected permanent config error, got %v", err)
			}
			reason, ok := operatorerrors.Reason(err)
			if !ok {
				t.Fatalf("expected reasoned error, got %v", err)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
			latest := &openbaov1alpha1.OpenBaoCluster{}
			if getErr := client.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, latest); getErr != nil {
				t.Fatalf("failed to get cluster: %v", getErr)
			}
			if latest.Status.OperationLock != nil {
				t.Fatalf("expected operation lock to be released, got %+v", latest.Status.OperationLock)
			}
			if cluster.Status.OperationLock != nil {
				t.Fatalf("expected in-memory operation lock to be released, got %+v", cluster.Status.OperationLock)
			}
		})
	}
}

func TestManager_MaybeHandleTargetRevisionDrift(t *testing.T) {
	t.Parallel()

	t.Run("early phase aborts and requeues when desired revision changes", func(t *testing.T) {
		scheme := newBlueGreenTestScheme(t)
		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		cluster.Status.BlueGreen.GreenRevision = staleGreenRevision
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    core.UpgradeOperationLockHolder,
			Message:   "blue/green upgrade phase Syncing",
		}

		greenWorkload := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)

		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithObjects(cluster, greenWorkload).
			Build()
		mgr := &Manager{client: client, scheme: scheme}

		handled, result, err := mgr.maybeHandleTargetRevisionDrift(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatal("expected handled=true")
		}
		if result.RequeueAfter != constants.RequeueShort {
			t.Fatalf("requeueAfter=%v, want %v", result.RequeueAfter, constants.RequeueShort)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
			t.Fatalf("phase = %s, want Idle", cluster.Status.BlueGreen.Phase)
		}
		if cluster.Status.BlueGreen.GreenRevision != "" {
			t.Fatalf("green revision = %q, want empty", cluster.Status.BlueGreen.GreenRevision)
		}
		if cluster.Status.OperationLock != nil {
			t.Fatalf("expected operation lock to be released, got %+v", cluster.Status.OperationLock)
		}
	})

	for _, phase := range []openbaov1alpha1.BlueGreenPhase{
		openbaov1alpha1.PhaseCleanup,
		openbaov1alpha1.PhaseRestoringReadReplicas,
	} {
		t.Run("completes current target when desired revision changes in "+string(phase), func(t *testing.T) {
			cluster := newBlueGreenCluster()
			cluster.Status.BlueGreen.Phase = phase
			cluster.Status.BlueGreen.GreenRevision = staleGreenRevision

			mgr := &Manager{}
			handled, _, err := mgr.maybeHandleTargetRevisionDrift(context.Background(), logr.Discard(), cluster)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handled {
				t.Fatal("expected handled=false so the current phase continues")
			}
			if cluster.Status.BlueGreen.Phase != phase {
				t.Fatalf("phase = %s, want %s", cluster.Status.BlueGreen.Phase, phase)
			}
			if cluster.Status.BlueGreen.RollbackStartTime != nil {
				t.Fatal("rollback started after Blue peer removal")
			}
		})
	}

	t.Run("late phase triggers rollback when desired revision changes", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseDemotingBlue
		cluster.Status.BlueGreen.GreenRevision = staleGreenRevision

		mgr := &Manager{}
		handled, result, err := mgr.maybeHandleTargetRevisionDrift(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatal("expected handled=true")
		}
		if result.RequeueAfter != constants.RequeueShort {
			t.Fatalf("requeueAfter=%v, want %v", result.RequeueAfter, constants.RequeueShort)
		}
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseRollingBack {
			t.Fatalf("phase = %s, want RollingBack", cluster.Status.BlueGreen.Phase)
		}
		if cluster.Status.BlueGreen.RollbackStartTime == nil {
			t.Fatal("expected rollback to be started")
		}
		if cluster.Status.BlueGreen.RollbackReason != upgrade.ReasonVersionMismatch {
			t.Fatalf("rollback reason = %q, want %q", cluster.Status.BlueGreen.RollbackReason, upgrade.ReasonVersionMismatch)
		}
	})
}

func TestManagerWithClientFactoryUsesProvidedFactory(t *testing.T) {
	t.Parallel()

	scheme := newBlueGreenTestScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	sentinel := errors.New("factory called")
	called := false
	factory := func(_ portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		called = true
		return nil, sentinel
	}

	mgr := newManagerWithClientFactory(client, scheme, nil, nil, factory, portopenbao.ClientConfig{}, nil, nil, "")
	if mgr.clusterOps == nil {
		t.Fatal("expected clusterOps to be initialized")
	}
	if _, err := mgr.clientFactory(portopenbao.ClientConfig{}); !errors.Is(err, sentinel) {
		t.Fatalf("expected custom client factory to be installed, got %v", err)
	}
	if !called {
		t.Fatal("expected custom client factory to be invoked")
	}
}

func TestRequeueAfter_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		want     recon.Result
	}{
		{
			name:     "negative duration returns zero result",
			duration: -1 * time.Second,
			want:     recon.Result{},
		},
		{
			name:     "zero duration returns zero result",
			duration: 0,
			want:     recon.Result{},
		},
		{
			name:     "positive duration requeues",
			duration: 3 * time.Second,
			want:     recon.Result{RequeueAfter: 3 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := requeueAfter(tt.duration); got != tt.want {
				t.Fatalf("requeueAfter(%v) = %v, want %v", tt.duration, got, tt.want)
			}
		})
	}
}

func TestManager_ReleaseUpgradeLockIfHeld(t *testing.T) {
	t.Parallel()

	t.Run("foreign lock is ignored", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationBackup,
			Holder:    "backup-controller",
			Message:   "backup running",
		}
		mgr := &Manager{}

		if err := mgr.releaseUpgradeLockIfHeld(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cluster.Status.OperationLock == nil {
			t.Fatalf("expected foreign lock to remain untouched")
		}
	})

	t.Run("owned lock is released", func(t *testing.T) {
		scheme := newBlueGreenTestScheme(t)
		cluster := newBlueGreenCluster()
		cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade,
			Holder:    core.UpgradeOperationLockHolder,
			Message:   "blue/green upgrade phase Syncing",
		}
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithObjects(cluster).
			Build()
		mgr := &Manager{client: client, scheme: scheme}

		if err := mgr.releaseUpgradeLockIfHeld(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cluster.Status.OperationLock != nil {
			t.Fatalf("expected upgrade lock to be released, got %+v", cluster.Status.OperationLock)
		}
	})
}

type workloadRuntimeStub struct {
	ensureBlueGreenStatusCalled bool
	lastCluster                 *openbaov1alpha1.OpenBaoCluster
	ensureStatefulSetCalled     bool
	lastConfigContent           string
	lastVerifiedImage           string
	lastVerifiedInitImage       string
	lastRevision                string
	lastDisableSelfInit         bool
	ensureStatefulSetErr        error
}

func (s *workloadRuntimeStub) EnsureBlueGreenStatus(_ context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) {
	s.ensureBlueGreenStatusCalled = true
	s.lastCluster = cluster
}

func (s *workloadRuntimeStub) EnsureStatefulSetWithRevision(_ context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, configContent string, verifiedImageDigest string, verifiedInitContainerDigest string, revision string, disableSelfInit bool) error {
	s.ensureStatefulSetCalled = true
	s.lastCluster = cluster
	s.lastConfigContent = configContent
	s.lastVerifiedImage = verifiedImageDigest
	s.lastVerifiedInitImage = verifiedInitContainerDigest
	s.lastRevision = revision
	s.lastDisableSelfInit = disableSelfInit
	return s.ensureStatefulSetErr
}

func TestManager_EnsureBlueGreenStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil workload runtime is a no-op", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		mgr := &Manager{}

		mgr.ensureBlueGreenStatus(context.Background(), logr.Discard(), cluster)
	})

	t.Run("delegates to workload runtime", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		runtime := &workloadRuntimeStub{}
		mgr := &Manager{workloadRuntime: runtime}

		mgr.ensureBlueGreenStatus(context.Background(), logr.Discard(), cluster)

		if !runtime.ensureBlueGreenStatusCalled {
			t.Fatal("expected EnsureBlueGreenStatus to be delegated to workload runtime")
		}
		if runtime.lastCluster != cluster {
			t.Fatal("expected workload runtime to receive the cluster being reconciled")
		}
	})
}

func TestManager_CalculateRevision(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	cluster := newBlueGreenCluster()

	first := mgr.calculateRevision(cluster)
	second := mgr.calculateRevision(cluster.DeepCopy())
	if first == "" {
		t.Fatal("expected non-empty revision")
	}
	if first != second {
		t.Fatalf("expected deterministic revision, got %q and %q", first, second)
	}

	modified := cluster.DeepCopy()
	modified.Spec.Image = "openbao/openbao:2.6.0"
	if got := mgr.calculateRevision(modified); got == first {
		t.Fatalf("expected revision to change when spec changes, still got %q", got)
	}
}

func TestManager_FinalizeBlueGreenMetrics_StateTransitions(t *testing.T) {
	t.Parallel()

	strategy := string(openbaov1alpha1.UpdateStrategyBlueGreen)

	t.Run("initializes state when upgrade starts", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Namespace = "metrics-start"
		cluster.Name = strings.ReplaceAll(t.Name(), "/", "-")
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
		core.DeleteUpgradeMetricsSession(cluster.Namespace, cluster.Name)
		defer core.DeleteUpgradeMetricsSession(cluster.Namespace, cluster.Name)

		mgr := &Manager{}
		mgr.finalizeBlueGreenMetrics(upgrade.NewMetrics(cluster.Namespace, cluster.Name), strategy, cluster, openbaov1alpha1.PhaseIdle, false)

		if _, ok := core.GetUpgradeMetricsSession(cluster.Namespace, cluster.Name); !ok {
			t.Fatal("expected upgrade metrics state to be initialized")
		}
	})

	t.Run("marks rollback when rollback starts", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Namespace = "metrics-rollback"
		cluster.Name = strings.ReplaceAll(t.Name(), "/", "-")
		now := metav1.NewTime(time.Now())
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseRollingBack
		cluster.Status.BlueGreen.RollbackStartTime = &now
		core.SetUpgradeMetricsSession(cluster.Namespace, cluster.Name, core.UpgradeMetricsSessionState{StartedAt: time.Now().Add(-2 * time.Minute)})
		defer core.DeleteUpgradeMetricsSession(cluster.Namespace, cluster.Name)

		mgr := &Manager{}
		mgr.finalizeBlueGreenMetrics(upgrade.NewMetrics(cluster.Namespace, cluster.Name), strategy, cluster, openbaov1alpha1.PhaseSyncing, false)

		state, ok := core.GetUpgradeMetricsSession(cluster.Namespace, cluster.Name)
		if !ok {
			t.Fatal("expected upgrade metrics state to exist")
		}
		if !state.RollbackSeen {
			t.Fatal("expected rollback marker to be recorded")
		}
	})

	t.Run("successful completion clears state", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Namespace = "metrics-success"
		cluster.Name = strings.ReplaceAll(t.Name(), "/", "-")
		cluster.Status.CurrentVersion = "2.4.4"
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle
		core.SetUpgradeMetricsSession(cluster.Namespace, cluster.Name, core.UpgradeMetricsSessionState{StartedAt: time.Now().Add(-5 * time.Minute)})

		mgr := &Manager{}
		mgr.finalizeBlueGreenMetrics(upgrade.NewMetrics(cluster.Namespace, cluster.Name), strategy, cluster, openbaov1alpha1.PhaseCleanup, false)

		if _, ok := core.GetUpgradeMetricsSession(cluster.Namespace, cluster.Name); ok {
			t.Fatal("expected upgrade metrics state to be cleared after successful completion")
		}
	})

	t.Run("failed completion clears state", func(t *testing.T) {
		cluster := newBlueGreenCluster()
		cluster.Namespace = "metrics-failure"
		cluster.Name = strings.ReplaceAll(t.Name(), "/", "-")
		cluster.Status.CurrentVersion = "2.4.4"
		cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle
		core.SetUpgradeMetricsSession(cluster.Namespace, cluster.Name, core.UpgradeMetricsSessionState{StartedAt: time.Now().Add(-5 * time.Minute)})

		mgr := &Manager{}
		mgr.finalizeBlueGreenMetrics(upgrade.NewMetrics(cluster.Namespace, cluster.Name), strategy, cluster, openbaov1alpha1.PhaseSyncing, false)

		if _, ok := core.GetUpgradeMetricsSession(cluster.Namespace, cluster.Name); ok {
			t.Fatal("expected upgrade metrics state to be cleared after failed completion")
		}
	})
}
