package openbaocluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	initmanagerport "github.com/kubebao/openbao-operator/internal/port/initmanager"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

const eventReasonAutopilotConfigJWTPrerequisitesMissing = "AutopilotConfigJWTPrerequisitesMissing"

// WorkloadResultPolicy configures known workload error handling behavior.
type WorkloadResultPolicy struct {
	PrerequisitesMissingReason   string
	GatewayAPIMissingReason      string
	PermanentConfigurationReason map[string]struct{}
	RequeueShort                 time.Duration
	RequeueSafetyNetBase         time.Duration
	RequeueSafetyNetJitter       time.Duration
	// UserActionReason lists errors that clear through a direct action on a managed
	// resource rather than a spec change, such as deleting a StatefulSet. Without
	// ownership watches nothing else would trigger the next reconcile, so they requeue.
	UserActionReason map[string]struct{}
}

// DefaultWorkloadResultPolicy returns the production workload error and
// requeue policy shared by controller entrypoints.
func DefaultWorkloadResultPolicy() WorkloadResultPolicy {
	return WorkloadResultPolicy{
		PrerequisitesMissingReason: constants.ReasonPrerequisitesMissing,
		GatewayAPIMissingReason:    constants.ReasonGatewayAPIMissing,
		RequeueShort:               constants.RequeueShort,
		RequeueSafetyNetBase:       constants.RequeueSafetyNetBase,
		RequeueSafetyNetJitter:     constants.RequeueSafetyNetJitter,
		PermanentConfigurationReason: map[string]struct{}{
			constants.ReasonInvalidVersion:                              {},
			constants.ReasonDowngradeBlocked:                            {},
			constants.ReasonImageVersionMismatch:                        {},
			constants.ReasonOIDCBootstrapConfigurationInvalid:           {},
			constants.ReasonAPIServerNetworkConfigurationInvalid:        {},
			constants.ReasonServiceIPFamilyChangeNotSupported:           {},
			constants.ReasonStorageInvalidSize:                          {},
			constants.ReasonStorageShrinkNotSupported:                   {},
			constants.ReasonStorageResizeNotSupported:                   {},
			constants.ReasonStorageClassChangeNotSupported:              {},
			constants.ReasonStorageRestartRequired:                      {},
			constants.ReasonAuditFileStorageStatefulSetRecreateRequired: {},
			constants.ReasonHelperImageConfigurationInvalid:             {},
		},
		UserActionReason: map[string]struct{}{
			constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired: {},
		},
	}
}

// AppendInitAndAutopilotReconcilers appends init and optional autopilot reconcilers.
func AppendInitAndAutopilotReconcilers(
	reconcilers []SubReconciler,
	initMgr initmanagerport.Manager,
	autopilotRuntime initmanagerport.AutopilotRuntime,
	statefulSetReader client.Reader,
	recorder events.EventRecorder,
	requeueShort time.Duration,
) []SubReconciler {
	if initMgr == nil {
		return reconcilers
	}

	reconcilers = append(reconcilers, initMgr)

	// Add autopilot config reconciler for Day 2 operations when an autopilot runtime is available.
	if autopilotRuntime != nil {
		reconcilers = append(reconcilers, &autopilotConfigReconciler{
			autopilotRuntime:  autopilotRuntime,
			statefulSetReader: statefulSetReader,
			recorder:          recorder,
			requeueShort: func() time.Duration {
				if requeueShort > 0 {
					return requeueShort
				}
				return 5 * time.Second
			}(),
		})
	}

	return reconcilers
}

// policyConfigReconciler reports failures without stopping workload reconciliation.
type policyConfigReconciler struct {
	manager  SubReconciler
	recorder events.EventRecorder
}

func (r *policyConfigReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	var previous *openbaov1alpha1.ControllerErrorStatus
	if cluster.Status.Workload != nil && cluster.Status.Workload.PolicyReconciliation != nil {
		previous = cluster.Status.Workload.PolicyReconciliation.LastError
	}
	result, err := r.manager.Reconcile(ctx, logger, cluster)
	if err != nil && r.recorder != nil && (previous == nil || previous.Message != err.Error()) {
		r.recorder.Eventf(cluster, nil, corev1.EventTypeWarning, constants.ReasonPolicyReconciliationFailed,
			"ReconcilePolicies", "%s", err.Error())
	}
	return result, err
}

// RunWorkloadReconcilers executes workload orchestration with consistent status patching.
func RunWorkloadReconcilers(
	ctx context.Context,
	c client.Client,
	logger logr.Logger,
	original *openbaov1alpha1.OpenBaoCluster,
	cluster *openbaov1alpha1.OpenBaoCluster,
	reconcilers []SubReconciler,
	policyReconciler SubReconciler,
	recordError ErrorRecorder,
	policy WorkloadResultPolicy,
) (recon.Result, error) {
	if cluster.Status.Workload == nil {
		cluster.Status.Workload = &openbaov1alpha1.WorkloadControllerStatus{}
	}
	result, err := runWorkloadSteps(ctx, logger, cluster, reconcilers, recordError, policy)
	// Run policy repair even when infrastructure or Autopilot returned early.
	// Otherwise missing operational permissions could prevent their own repair.
	if policyReconciler != nil {
		policyResult, _ := policyReconciler.Reconcile(ctx, logger, cluster)
		if policyResult.RequeueAfter > 0 && (result.RequeueAfter <= 0 || policyResult.RequeueAfter < result.RequeueAfter) {
			result.RequeueAfter = policyResult.RequeueAfter
		}
	}
	if statusErr := PatchWorkloadOwnedFields(ctx, c, logger, original, cluster, "workload-reconcile"); statusErr != nil {
		return recon.Result{}, statusErr
	}
	return result, err
}

func runWorkloadSteps(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	reconcilers []SubReconciler,
	recordError ErrorRecorder,
	policy WorkloadResultPolicy,
) (recon.Result, error) {
	for _, rec := range reconcilers {
		result, err := rec.Reconcile(ctx, logger, cluster)
		if err != nil {
			if recordError != nil {
				recordError(err)
			}
			cluster.Status.Workload.LastError = controllerErrorStatus(err)
			if handled, ok := workloadResultForError(err, cluster.Status.Workload.LastError, policy); ok {
				return handled, nil
			}
			return recon.Result{}, err
		}
		if result.RequeueAfter > 0 {
			return result, nil
		}
	}
	// Clear previous workload error only after a complete successful pass.
	cluster.Status.Workload.LastError = nil
	return recon.Result{}, nil
}

func workloadResultForError(
	err error,
	lastError *openbaov1alpha1.ControllerErrorStatus,
	policy WorkloadResultPolicy,
) (recon.Result, bool) {
	if lastError != nil {
		switch lastError.Reason {
		case policy.PrerequisitesMissingReason:
			return recon.Result{RequeueAfter: workloadRequeueShort(policy)}, true
		case policy.GatewayAPIMissingReason:
			safetyNetJitter := workloadRequeueSafetyNetJitter(policy)
			jitterNanos := time.Now().UnixNano() % int64(safetyNetJitter)
			requeueAfter := workloadRequeueSafetyNetBase(policy) + time.Duration(jitterNanos)
			return recon.Result{RequeueAfter: requeueAfter}, true
		default:
			if _, ok := policy.UserActionReason[lastError.Reason]; ok {
				return recon.Result{RequeueAfter: workloadRequeueShort(policy)}, true
			}
			if _, ok := policy.PermanentConfigurationReason[lastError.Reason]; ok {
				// Permanent configuration issue; wait for user changes rather than hot-looping.
				return recon.Result{}, true
			}
		}
	}

	if operatorerrors.IsTransient(err) {
		shouldRequeue, requeueAfter := operatorerrors.ShouldRequeue(err)
		if shouldRequeue {
			if requeueAfter > 0 {
				return recon.Result{RequeueAfter: requeueAfter}, true
			}
			return recon.Result{RequeueAfter: workloadRequeueShort(policy)}, true
		}
	}

	return recon.Result{}, false
}

// autopilotConfigReconciler reconciles Raft Autopilot configuration for initialized clusters.
// This handles Day 2 operations like scaling replicas or changing autopilot settings.
type autopilotConfigReconciler struct {
	autopilotRuntime  initmanagerport.AutopilotRuntime
	statefulSetReader client.Reader
	recorder          events.EventRecorder
	requeueShort      time.Duration
}

// Reconcile reconciles the Raft Autopilot configuration for an initialized cluster.
func (r *autopilotConfigReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	// During upgrades we expect transient API instability (pod restarts, leader changes).
	// Autopilot reconciliation uses authenticated API calls against the stable public
	// Service and can create unnecessary load and noisy failures while the cluster
	// is rolling. Skip it until the upgrade finishes.
	if cluster != nil && cluster.Status.Upgrade != nil {
		logger.V(1).Info("Skipping autopilot config reconciliation during upgrade")
		return recon.Result{}, nil
	}

	autopilotCluster, err := r.autopilotTargetCluster(ctx, logger, cluster)
	if err != nil {
		if operatorerrors.IsTransient(err) {
			logger.V(1).Info("Transient error determining autopilot config target; will retry", "error", err)
			return recon.Result{RequeueAfter: r.requeueShort}, nil
		}

		logger.Error(err, "Failed to determine autopilot config target (non-transient)")
		return recon.Result{}, nil
	}

	if err := r.autopilotRuntime.ReconcileAutopilotConfig(ctx, logger, autopilotCluster); err != nil {
		if operatorerrors.IsTransient(err) {
			logger.V(1).Info("Transient error reconciling autopilot config; will retry", "error", err)
			return recon.Result{RequeueAfter: r.requeueShort}, nil
		}

		// Check if this is a permanent prerequisites missing error.
		if operatorerrors.IsPermanent(err) &&
			errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
			// Emit Warning event with actionable guidance
			// Note: SelfInit only works during initial startup, so if cluster is initialized,
			// users must configure JWT manually via API/CLI or configure autopilot in CRD.
			var eventMsg string
			if cluster.Status.Initialized {
				eventMsg = "Autopilot configuration requires JWT authentication. " +
					"Since this operator-managed cluster is already initialized, SelfInit is no longer available. " +
					"Manually configure JWT authentication via OpenBao API/CLI, " +
					"or manually configure autopilot settings in spec.configuration.raft.autopilot. " +
					"Error: %v"
			} else {
				eventMsg = "Autopilot configuration requires JWT authentication. " +
					"Enable JWT auth via spec.selfInit.oidc.enabled: true or configure JWT via self-init requests during initialization. " +
					"Alternatively, manually configure autopilot settings in spec.configuration.raft.autopilot. " +
					"Error: %v"
			}
			r.recorder.Eventf(cluster, nil, corev1.EventTypeWarning,
				eventReasonAutopilotConfigJWTPrerequisitesMissing, eventReasonAutopilotConfigJWTPrerequisitesMissing,
				eventMsg, err)
			logger.Error(err, "Failed to reconcile autopilot config (permanent error - requires user intervention)")
			return recon.Result{}, nil
		}

		// Other non-transient errors.
		logger.Error(err, "Failed to reconcile autopilot config (non-transient)")
		return recon.Result{}, nil
	}

	return recon.Result{}, nil
}

func (r *autopilotConfigReconciler) autopilotTargetCluster(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (*openbaov1alpha1.OpenBaoCluster, error) {
	if cluster == nil || r.statefulSetReader == nil {
		return cluster, nil
	}

	statefulSetName := cluster.Name
	if stableRevision := workloadsvc.BlueGreenStableRevision(cluster); stableRevision != "" {
		statefulSetName = fmt.Sprintf("%s-%s", cluster.Name, stableRevision)
	}

	currentStatefulSet := &appsv1.StatefulSet{}
	err := r.statefulSetReader.Get(
		ctx,
		client.ObjectKey{Name: statefulSetName, Namespace: cluster.Namespace},
		currentStatefulSet,
	)
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		return cluster, nil
	default:
		return nil, operatorerrors.WrapTransientKubernetesAPI(
			fmt.Errorf(
				"failed to get StatefulSet %s/%s for autopilot reconciliation: %w",
				cluster.Namespace,
				statefulSetName,
				err,
			),
		)
	}

	if currentStatefulSet.Spec.Replicas == nil {
		return cluster, nil
	}

	appliedReplicas := *currentStatefulSet.Spec.Replicas
	if cluster.Spec.Replicas >= appliedReplicas {
		return cluster, nil
	}

	logger.V(1).Info(
		"Using staged StatefulSet replica count for autopilot reconciliation during scale down",
		"desiredReplicas", cluster.Spec.Replicas,
		"appliedReplicas", appliedReplicas,
		"statefulSet", fmt.Sprintf("%s/%s", currentStatefulSet.Namespace, currentStatefulSet.Name),
	)

	autopilotCluster := cluster.DeepCopy()
	autopilotCluster.Spec.Replicas = appliedReplicas
	return autopilotCluster, nil
}

func workloadRequeueShort(policy WorkloadResultPolicy) time.Duration {
	if policy.RequeueShort > 0 {
		return policy.RequeueShort
	}
	return 5 * time.Second
}

func workloadRequeueSafetyNetBase(policy WorkloadResultPolicy) time.Duration {
	if policy.RequeueSafetyNetBase > 0 {
		return policy.RequeueSafetyNetBase
	}
	return 20 * time.Minute
}

func workloadRequeueSafetyNetJitter(policy WorkloadResultPolicy) time.Duration {
	if policy.RequeueSafetyNetJitter > 0 {
		return policy.RequeueSafetyNetJitter
	}
	return 5 * time.Minute
}
