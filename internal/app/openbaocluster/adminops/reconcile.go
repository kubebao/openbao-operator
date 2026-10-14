package adminops

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	backupmanager "github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
	upgrademanager "github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/bluegreen"
	rollingupgrade "github.com/kubebao/openbao-operator/internal/service/upgrade/rolling"
	workloadmanager "github.com/kubebao/openbao-operator/internal/service/workload"
)

// Dependencies holds dependencies required to build admin operations reconcilers.
type Dependencies struct {
	Client                client.Client
	APIReader             client.Reader
	Scheme                *runtime.Scheme
	Recorder              events.EventRecorder
	SmartClientConfig     portopenbao.ClientConfig
	ImageVerifier         imageverify.Verifier
	OperatorImageVerifier imageverify.Verifier
	RequeueShort          time.Duration
	Platform              string
}

// ErrorRecorder captures errors for metric bookkeeping in controller wrappers.
type ErrorRecorder func(error)

// StatusPatcher persists adminops-owned status fields.
type StatusPatcher func(
	ctx context.Context,
	logger logr.Logger,
	original *openbaov1alpha1.OpenBaoCluster,
	cluster *openbaov1alpha1.OpenBaoCluster,
	acknowledgements upgrademanager.RequestAcknowledgements,
	reason string,
) error

// ErrorStatusBuilder creates status payloads from reconciliation errors.
type ErrorStatusBuilder func(error) *openbaov1alpha1.ControllerErrorStatus

type subReconciler interface {
	Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (upgrademanager.ReconcileResult, error)
}

// reconcilerWithoutRequests adapts collaborators that only return scheduling.
type reconcilerWithoutRequests struct {
	inner interface {
		Reconcile(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error)
	}
}

func (r reconcilerWithoutRequests) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (upgrademanager.ReconcileResult, error) {
	result, err := r.inner.Reconcile(ctx, logger, cluster)
	return upgrademanager.ReconcileResult{Result: result}, err
}

type reconcilerPlan struct {
	upgradeReconcilers []subReconciler
	backupReconciler   subReconciler
}

// Application executes the prebuilt admin-operations reconciliation plan.
type Application struct {
	plan         reconcilerPlan
	requeueShort time.Duration
	patchStatus  StatusPatcher
	errorStatus  ErrorStatusBuilder
}

// NewApplication builds the admin-operations collaborators used by every
// reconciliation pass.
func NewApplication(
	deps Dependencies,
	patchStatus StatusPatcher,
	errorStatus ErrorStatusBuilder,
) *Application {
	if errorStatus == nil {
		errorStatus = defaultErrorStatus
	}

	return &Application{
		plan:         buildReconcilers(deps),
		requeueShort: resolveRequeueShort(deps.RequeueShort),
		patchStatus:  patchStatus,
		errorStatus:  errorStatus,
	}
}

func (p reconcilerPlan) orderedFor(cluster *openbaov1alpha1.OpenBaoCluster) []subReconciler {
	reconcilers := make([]subReconciler, 0, len(p.upgradeReconcilers)+1)
	withPolicy := func(rec subReconciler, policy string) subReconciler {
		if !portauth.PolicyReconciliationEnabled(cluster) {
			return rec
		}
		return policyReadinessReconciler{inner: rec, policy: policy}
	}
	retentionRunning := cluster != nil && cluster.Status.Backup != nil && cluster.Status.Backup.Retention != nil &&
		cluster.Status.Backup.Retention.Active != nil
	backupOwnsLock := retentionRunning || (cluster != nil &&
		cluster.Status.OperationLock != nil &&
		cluster.Status.OperationLock.Operation == openbaov1alpha1.ClusterOperationBackup)

	if backupOwnsLock && p.backupReconciler != nil {
		reconcilers = append(reconcilers, withPolicy(p.backupReconciler, portauth.PolicyNameBackup))
	}
	for _, rec := range p.upgradeReconcilers {
		reconcilers = append(reconcilers, withPolicy(rec, portauth.PolicyNameUpgrade))
	}
	if !backupOwnsLock && p.backupReconciler != nil {
		reconcilers = append(reconcilers, withPolicy(p.backupReconciler, portauth.PolicyNameBackup))
	}
	return reconcilers
}

type policyReadinessReconciler struct {
	inner  subReconciler
	policy string
}

func (r policyReadinessReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (upgrademanager.ReconcileResult, error) {
	// Check before each reconciler: a preceding operation can release its lock
	// during this pass. Running operations can still observe Jobs and finish.
	retentionRunning := r.policy == portauth.PolicyNameBackup && cluster.Status.Backup != nil &&
		cluster.Status.Backup.Retention != nil && cluster.Status.Backup.Retention.Active != nil
	if cluster.Status.OperationLock == nil && !retentionRunning {
		if err := configuration.RequirePolicyReady(cluster, r.policy); err != nil {
			return upgrademanager.ReconcileResult{}, err
		}
	}
	return r.inner.Reconcile(ctx, logger, cluster)
}

// Reconcile executes admin-operations orchestration and status patching.
func (a *Application) Reconcile(
	ctx context.Context,
	logger logr.Logger,
	original *openbaov1alpha1.OpenBaoCluster,
	cluster *openbaov1alpha1.OpenBaoCluster,
	recordError ErrorRecorder,
) (recon.Result, error) {
	ensureAdminOpsStatus(cluster)
	var acknowledgements upgrademanager.RequestAcknowledgements
	var policyError error

	for _, rec := range a.plan.orderedFor(cluster) {
		result, err := rec.Reconcile(ctx, logger, cluster)
		acknowledgements.Merge(result.Acknowledgements)
		if reason, _ := operatorerrors.Reason(err); reason == constants.ReasonPoliciesNotReady {
			// A blocked upgrade must not suppress an independently approved backup.
			if policyError == nil {
				policyError = err
			}
			continue
		}
		if err != nil {
			if recordError != nil {
				recordError(err)
			}
			ensureAdminOpsStatus(cluster)
			cluster.Status.AdminOps.LastError = a.errorStatus(err)

			if a.patchStatus != nil {
				if statusErr := a.patchStatus(ctx, logger, original, cluster, acknowledgements, "adminops-error"); statusErr != nil {
					return recon.Result{}, statusErr
				}
			}

			if operatorerrors.IsTransient(err) {
				shouldRequeue, requeueAfter := operatorerrors.ShouldRequeue(err)
				if shouldRequeue {
					if requeueAfter > 0 {
						return recon.Result{RequeueAfter: requeueAfter}, nil
					}
					return recon.Result{RequeueAfter: a.requeueShort}, nil
				}
			}
			if operatorerrors.IsPermanent(err) {
				return recon.Result{}, err
			}
			return recon.Result{}, err
		}

		if result.RequeueAfter > 0 {
			if policyError != nil {
				result.RequeueAfter = min(result.RequeueAfter, time.Minute)
			}
			// A requeue without an error is a successful observation. Clear any
			// error from an earlier pass before persisting the next poll time.
			ensureAdminOpsStatus(cluster)
			cluster.Status.AdminOps.LastError = a.errorStatus(policyError)
			if a.patchStatus != nil {
				if statusErr := a.patchStatus(ctx, logger, original, cluster, acknowledgements, "adminops-requeue"); statusErr != nil {
					return recon.Result{}, statusErr
				}
			}
			return recon.Result{RequeueAfter: result.RequeueAfter}, nil
		}
	}

	// Clear previous adminops error after a successful reconcile.
	ensureAdminOpsStatus(cluster)
	cluster.Status.AdminOps.LastError = a.errorStatus(policyError)
	if a.patchStatus != nil {
		if err := a.patchStatus(ctx, logger, original, cluster, acknowledgements, "adminops-complete"); err != nil {
			return recon.Result{}, fmt.Errorf("failed to patch adminops owned fields: %w", err)
		}
	}

	if policyError != nil {
		return recon.Result{RequeueAfter: time.Minute}, nil
	}
	return recon.Result{}, nil
}

func ensureAdminOpsStatus(cluster *openbaov1alpha1.OpenBaoCluster) {
	if cluster == nil {
		return
	}
	if cluster.Status.AdminOps == nil {
		cluster.Status.AdminOps = &openbaov1alpha1.AdminOpsControllerStatus{}
	}
}

func buildReconcilers(deps Dependencies) reconcilerPlan {
	workloadMgr := workloadmanager.NewManager(deps.Client, deps.Scheme, deps.Platform).WithReader(deps.APIReader)
	backupRuntime := backupmanager.NewUpgradeStrategyRuntime(deps.Client, deps.Scheme)
	strategyReader := deps.APIReader
	if strategyReader == nil {
		strategyReader = deps.Client
	}
	adminOpsMutator := adminopsstatus.NewMutator(deps.APIReader, deps.Client)

	return reconcilerPlan{
		upgradeReconcilers: []subReconciler{
			reconcilerWithoutRequests{inner: upgrademanager.NewStrategyTransitionManager(strategyReader)},
			bluegreen.NewManager(
				deps.Client,
				deps.Scheme,
				workloadMgr,
				backupRuntime,
				deps.SmartClientConfig,
				deps.ImageVerifier,
				deps.OperatorImageVerifier,
				deps.Platform,
				deps.Recorder,
			).WithReader(deps.APIReader).WithAdminOpsStatusMutator(adminOpsMutator),
			rollingupgrade.NewManager(
				deps.Client,
				deps.Scheme,
				backupRuntime,
				deps.SmartClientConfig,
				deps.OperatorImageVerifier,
				deps.Platform,
				deps.Recorder,
			).WithReader(deps.APIReader).WithAdminOpsStatusMutator(adminOpsMutator),
		},
		backupReconciler: reconcilerWithoutRequests{inner: backupmanager.NewManager(
			deps.Client,
			deps.Scheme,
			deps.SmartClientConfig,
			deps.OperatorImageVerifier,
			deps.Platform,
			deps.Recorder,
		).WithReader(deps.APIReader).WithAdminOpsStatusMutator(adminOpsMutator)},
	}
}

func resolveRequeueShort(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return 5 * time.Second
}

func defaultErrorStatus(err error) *openbaov1alpha1.ControllerErrorStatus {
	if err == nil {
		return nil
	}

	reason := "Error"
	if mappedReason, ok := operatorerrors.Reason(err); ok {
		reason = mappedReason
	}
	now := metav1.Now()
	return &openbaov1alpha1.ControllerErrorStatus{
		Reason:  reason,
		Message: err.Error(),
		At:      &now,
	}
}
