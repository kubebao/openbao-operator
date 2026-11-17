package rolling

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	portbackup "github.com/kubebao/openbao-operator/internal/port/backup"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/raftops"
)

var (
	// ErrNoUpgradeToken indicates that no suitable upgrade JWT role is configured.
	ErrNoUpgradeToken = errors.New("no upgrade JWT role configured: set spec.upgrade.jwtAuthRole or enable spec.selfInit.oidc.enabled")
)

// Manager reconciles version and Raft-aware upgrade behavior for an OpenBaoCluster.
type Manager struct {
	client                client.Client
	reader                client.Reader
	scheme                *runtime.Scheme
	backupRuntime         portbackup.PreUpgradeSnapshotRuntime
	recorder              events.EventRecorder
	clientFactory         raftops.OpenBaoClientFactory
	clientConfig          portopenbao.ClientConfig
	operatorImageVerifier imageverify.Verifier
	adminOpsMutator       adminops.StatusMutator
	voterRolloutGuard     portworkload.VoterRolloutGuard
	Platform              string
}

// NewManager constructs a Manager that uses the provided Kubernetes client and scheme.
func NewManager(
	c client.Client,
	scheme *runtime.Scheme,
	backupRuntime portbackup.PreUpgradeSnapshotRuntime,
	clientConfig portopenbao.ClientConfig,
	operatorImageVerifier imageverify.Verifier,
	platform string,
	recorder ...events.EventRecorder,
) *Manager {
	var eventRecorder events.EventRecorder
	if len(recorder) > 0 {
		eventRecorder = recorder[0]
	}
	return &Manager{
		client:                c,
		reader:                c,
		scheme:                scheme,
		backupRuntime:         backupRuntime,
		recorder:              eventRecorder,
		clientFactory:         raftops.DefaultOpenBaoClientFactory,
		clientConfig:          clientConfig,
		operatorImageVerifier: operatorImageVerifier,
		Platform:              platform,
	}
}

// WithReader configures a live reader for status read-before-write flows.
func (m *Manager) WithReader(reader client.Reader) *Manager {
	if reader != nil {
		m.reader = reader
	}
	return m
}

// WithVoterRolloutGuard configures the shared Raft catch-up guard.
func (m *Manager) WithVoterRolloutGuard(guard portworkload.VoterRolloutGuard) *Manager {
	m.voterRolloutGuard = guard
	return m
}

func (m *Manager) jobReader() client.Reader {
	if m.reader != nil {
		return m.reader
	}
	return m.client
}

// WithAdminOpsStatusMutator configures the adminops-plane status persistence hook.
func (m *Manager) WithAdminOpsStatusMutator(mutator adminops.StatusMutator) *Manager {
	if mutator != nil {
		m.adminOpsMutator = mutator
	}
	return m
}

// Reconcile ensures upgrades progress safely for the given OpenBaoCluster.
//
// The upgrade state machine follows these phases:
//  1. Detection: Check if upgrade is needed or if we're resuming an existing one
//  2. Pre-upgrade Validation: Validate version, check cluster health
//  3. Initialize Upgrade: Set up upgrade state, lock StatefulSet partition
//  4. Pod-by-Pod Update: Step down leader if needed, update each pod in reverse ordinal order
//  5. Finalization: Clear upgrade state, update current version
//
// Returns the reconcile result for follow-up scheduling along with any error.
func (m *Manager) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (outcome upgrade.ReconcileResult, err error) {
	outcome.Result, err = m.reconcile(ctx, logger, cluster, &outcome.Acknowledgements)
	return outcome, err
}

func (m *Manager) reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, acknowledgements *upgrade.RequestAcknowledgements) (recon.Result, error) {
	logger = logger.WithValues(
		"specVersion", cluster.Spec.Version,
		"statusVersion", cluster.Status.CurrentVersion,
	)

	metrics := upgrade.NewMetrics(cluster.Namespace, cluster.Name)
	strategy := string(upgrade.EffectiveStrategy(cluster))

	if result, done := m.shouldSkipUpgradeReconcile(logger, cluster); done {
		return result, nil
	}
	if err := portopenbao.ValidateSealPlugins(cluster); err != nil {
		return recon.Result{}, operatorerrors.WrapPermanentConfig(err)
	}

	decision := decideUpgrade(cluster)
	acknowledgements.Merge(decision.acknowledgements)
	if decision.action != upgradeIdle {
		logger.Info("Selected rolling upgrade action", "action", decision.action,
			"failureReason", upgrade.UpgradeFailureReason(cluster.Status.Upgrade),
			"retryRequestField", upgrade.RequestRetryFieldPath)
	}
	if result, done, err := m.ensureUpgradeLock(ctx, logger, cluster, metrics, strategy, decision.action); done || err != nil {
		return result, err
	}

	if result, done, err := m.prepareUpgradeExecution(ctx, logger, cluster, metrics, strategy, decision); done || err != nil {
		return result, err
	}

	return m.reconcileUpgradeExecution(ctx, logger, cluster, metrics, strategy)
}
