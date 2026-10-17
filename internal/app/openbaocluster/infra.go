package openbaocluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	initmanagerport "github.com/kubebao/openbao-operator/internal/port/initmanager"
	bootstrapmanager "github.com/kubebao/openbao-operator/internal/service/bootstrap"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

const (
	defaultImageVerificationFailurePolicyBlock = "Block"

	infraOpenBaoImageRepoEnv = "RELATED_IMAGE_OPENBAO"
	infraDefaultOpenBaoImage = "openbao/openbao"

	infraImageVerificationTimeout = 5 * time.Second
	infraRequeueShort             = 5 * time.Second
)

type verifyImageFunc func(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, imageRef string) (string, error)
type verifyOperatorImageFunc func(ctx context.Context, logger logr.Logger, verifier imageverify.Verifier, cluster *openbaov1alpha1.OpenBaoCluster, imageRef string) (string, error)
type imageVerificationEnabledFunc func(cluster *openbaov1alpha1.OpenBaoCluster) bool
type oidcDiscoveryStatusCodeFunc func(err error) (int, bool)
type ScaleDownPodClient interface {
	IsLeader(ctx context.Context) (bool, error)
	StepDownLeader(ctx context.Context) error
}
type ScaleDownPodClientFactory func(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (ScaleDownPodClient, error)
type discoverOIDCConfigFunc func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error)

// InfraKubernetesRuntime groups Kubernetes-facing collaborators used by infra reconciliation.
type InfraKubernetesRuntime struct {
	Client            client.Client
	APIReader         client.Reader
	Scheme            *runtime.Scheme
	OperatorNamespace string
	Platform          string
}

// InfraOIDCRuntime groups OIDC discovery inputs and warmup state.
type InfraOIDCRuntime struct {
	RestConfig          *rest.Config
	OIDCIssuer          string
	OIDCDiscoveryURL    string
	OIDCDiscoveryCAPEM  string
	OIDCJWKSURL         string
	OIDCJWKSCAPEM       string
	OIDCJWTKeys         []string
	DiscoverOIDCConfig  discoverOIDCConfigFunc
	DiscoveryStatusCode oidcDiscoveryStatusCodeFunc
}

// InfraImageVerificationRuntime groups image verification collaborators.
type InfraImageVerificationRuntime struct {
	OperatorImageVerifier              imageverify.Verifier
	VerifyImageFunc                    verifyImageFunc
	VerifyOperatorImage                verifyOperatorImageFunc
	IsMainImageVerificationEnabled     imageVerificationEnabledFunc
	IsOperatorImageVerificationEnabled imageVerificationEnabledFunc
}

// InfraEventRuntime groups event emission dependencies.
type InfraEventRuntime struct {
	Recorder events.EventRecorder
}

// InfraPodRuntime groups pod-targeted OpenBao client construction.
type InfraPodRuntime struct {
	ClientForPodFunc ScaleDownPodClientFactory
}

// InfraScaleDownRuntime groups authenticated Raft membership operations used
// to stage safe scale-downs.
type InfraScaleDownRuntime struct {
	Runtime            initmanagerport.ScaleDownRuntime
	ReadReplicaRuntime initmanagerport.ReadReplicaScaleDownRuntime
	Membership         StatusMembershipRuntime
}

// InfraDependencies provides external dependencies for infrastructure reconciliation.
type InfraDependencies struct {
	Kubernetes        InfraKubernetesRuntime
	OIDC              InfraOIDCRuntime
	ImageVerification InfraImageVerificationRuntime
	Events            InfraEventRuntime
	Pods              InfraPodRuntime
	ScaleDown         InfraScaleDownRuntime
}

type infraReconciler struct {
	deps InfraDependencies
}

// NewInfraReconciler creates a SubReconciler that handles infrastructure orchestration.
func NewInfraReconciler(deps InfraDependencies) SubReconciler {
	return &infraReconciler{deps: deps}
}

// Reconcile implements the controller's sub-reconciler contract for infrastructure reconciliation.
func (r *infraReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	logger.V(1).Info("Reconciling infrastructure for OpenBaoCluster")

	if err := upgrade.ValidateUpgradeTargetVersion(logger, cluster.Status.CurrentVersion, cluster.Spec.Version); err != nil {
		return recon.Result{}, err
	}
	if err := upgrade.ValidateImageRefMatchesVersion(cluster.Spec.Version, cluster.Spec.Image); err != nil {
		return recon.Result{}, err
	}

	resolvedImages, err := r.resolveVerifiedImages(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, err
	}

	spec := r.computeStatefulSetSpec(logger, cluster, resolvedImages.mainImage, resolvedImages.initImage)
	readSpec := r.computeReadReplicaStatefulSetSpec(cluster, resolvedImages.mainImage, resolvedImages.initImage)
	stagedScaleDown := false
	stagedReadReplicaScaleDown := false
	stagedRestartOrdering := false
	applyOperationalReadReplicaStageDown(cluster, &readSpec)

	currentSTS := &appsv1.StatefulSet{}
	readCurrentSTS := &appsv1.StatefulSet{}
	currentSTSFound := false
	readCurrentSTSFound := false
	reader := r.deps.Kubernetes.APIReader
	if reader == nil {
		reader = r.deps.Kubernetes.Client
	}

	err = reader.Get(ctx, client.ObjectKey{Name: spec.Name, Namespace: cluster.Namespace}, currentSTS)
	switch {
	case err == nil:
		currentSTSFound = true
		if r.handleInterruptedScaleDown(ctx, logger, cluster, currentSTS, spec.Replicas, false) {
			return recon.Result{RequeueAfter: infraRequeueShort}, nil
		}
		appliedReplicas, err := r.handleScaleDownSafety(ctx, cluster, spec.Replicas, currentSTS)
		if err != nil {
			return scaleDownWaitResult(logger, err, false), nil
		}
		spec.Replicas = appliedReplicas
		stagedScaleDown = cluster.Status.Initialized && spec.Replicas > cluster.Spec.Replicas
	case apierrors.IsNotFound(err):
		// No StatefulSet exists yet; scale-down safety only applies once the workload is created.
	default:
		return recon.Result{}, operatorerrors.WrapTransientKubernetesAPI(
			err,
		)
	}

	err = reader.Get(ctx, client.ObjectKey{Name: readSpec.Name, Namespace: cluster.Namespace}, readCurrentSTS)
	switch {
	case err == nil:
		readCurrentSTSFound = true
		declaredReadReplicas := readSpec.Replicas
		if r.handleInterruptedScaleDown(ctx, logger, cluster, readCurrentSTS, declaredReadReplicas, true) {
			return recon.Result{RequeueAfter: infraRequeueShort}, nil
		}
		appliedReplicas, err := r.handleReadReplicaScaleDownSafety(ctx, cluster, declaredReadReplicas, readCurrentSTS)
		if err != nil {
			return scaleDownWaitResult(logger, err, true), nil
		}
		readSpec.Replicas = appliedReplicas
		stagedReadReplicaScaleDown = cluster.Status.Initialized && readSpec.Replicas > declaredReadReplicas
	case apierrors.IsNotFound(err):
		// No read-replica StatefulSet exists yet.
	default:
		return recon.Result{}, operatorerrors.WrapTransientKubernetesAPI(err)
	}

	stagedRestartOrdering = r.applyReadFirstRestartOrdering(cluster, &spec, &readSpec, currentSTS, currentSTSFound, readCurrentSTS, readCurrentSTSFound)

	effectiveOIDC, err := r.resolveOIDC(ctx, cluster)
	if err != nil {
		return recon.Result{}, err
	}

	manager := r.newBootstrapManager(effectiveOIDC)
	configContent, err := manager.PrepareWorkload(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, r.mapManagerReconcileError(err)
	}
	if err := r.newNetworkingManager().Reconcile(ctx, logger, cluster); err != nil {
		return recon.Result{}, r.mapManagerReconcileError(err)
	}
	if err := r.newIdentityManager().Reconcile(ctx, logger, cluster); err != nil {
		return recon.Result{}, r.mapManagerReconcileError(err)
	}
	if err := r.newWorkloadManager().Reconcile(ctx, logger, cluster, configContent, spec); err != nil {
		return recon.Result{}, r.mapManagerReconcileError(err)
	}
	if readSpec.SkipReconciliation {
		shouldRequeue, err := r.reconcileDisabledReadReplicas(ctx, logger, cluster, readSpec, readCurrentSTS, readCurrentSTSFound)
		if err != nil {
			return recon.Result{}, r.mapManagerReconcileError(err)
		}
		if shouldRequeue {
			return recon.Result{RequeueAfter: infraRequeueShort}, nil
		}
	} else {
		readConfigContent, err := manager.RenderConfig(cluster, bootstrapReadReplicaRenderOptions(cluster, spec))
		if err != nil {
			return recon.Result{}, err
		}
		if err := r.newWorkloadManager().Reconcile(ctx, logger, cluster, readConfigContent, readSpec); err != nil {
			return recon.Result{}, r.mapManagerReconcileError(err)
		}
	}
	if stagedScaleDown {
		logger.Info(
			"Staged scale down step applied; requeueing to continue safe replica reduction",
			"appliedStatefulSetReplicas", spec.Replicas,
			"desiredReplicas", cluster.Spec.Replicas,
		)
		return recon.Result{RequeueAfter: infraRequeueShort}, nil
	}
	if stagedReadReplicaScaleDown {
		logger.Info(
			"Staged read-replica scale down step applied; requeueing to continue safe replica reduction",
			"appliedStatefulSetReplicas", readSpec.Replicas,
			"desiredReplicas", readCurrentDesiredReplicas(cluster),
		)
		return recon.Result{RequeueAfter: infraRequeueShort}, nil
	}
	if stagedRestartOrdering {
		logger.Info("Read-pool restart ordering is still in progress; requeueing before voter restart rollout continues")
		return recon.Result{RequeueAfter: infraRequeueShort}, nil
	}

	return recon.Result{}, nil
}

func readCurrentDesiredReplicas(cluster *openbaov1alpha1.OpenBaoCluster) int32 {
	if cluster == nil || cluster.Spec.ReadReplicas == nil {
		return 0
	}
	return cluster.Spec.ReadReplicas.Replicas
}

func bootstrapReadReplicaRenderOptions(cluster *openbaov1alpha1.OpenBaoCluster, voterSpec workloadsvc.StatefulSetSpec) bootstrapmanager.RenderOptions {
	return bootstrapmanager.RenderOptions{
		RetryJoinLabelSelector: selectorString(resourceidentity.VoterPodSelectorLabelsWithRevision(cluster, voterSpec.Revision)),
		RetryJoinAsNonVoter:    true,
	}
}

func selectorString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return strings.Join(parts, ",")
}
