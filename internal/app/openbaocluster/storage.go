package openbaocluster

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

const (
	eventReasonPVCResize               = "PVCResize"
	eventReasonPVCResizeLeaderStepDown = "PVCResizeLeaderStepDown"
	eventReasonPVCResizePodRestart     = "PVCResizePodRestart"
	storageVolumeDataPrefix            = "data-"
	storageRequeueShort                = 5 * time.Second
)

// StorageResourceRuntime groups Kubernetes clients used by storage reconciliation.
type StorageResourceRuntime struct {
	Client    client.Client
	APIReader client.Reader
}

// StorageEventRuntime groups event emission dependencies for storage workflows.
type StorageEventRuntime struct {
	Recorder events.EventRecorder
}

// StorageDependencies provides external dependencies for storage reconciliation.
type StorageDependencies struct {
	Resources StorageResourceRuntime
	Events    StorageEventRuntime
}

type storageReconciler struct {
	deps StorageDependencies
}

// NewStorageReconciler creates a SubReconciler that handles PVC storage expansion workflows.
func NewStorageReconciler(deps StorageDependencies) SubReconciler {
	return &storageReconciler{deps: deps}
}

func (r *storageReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	return ReconcileStorage(ctx, logger, cluster, r.deps)
}

// ReconcileStorage validates and applies supported PVC storage expansion changes.
func ReconcileStorage(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	deps StorageDependencies,
) (recon.Result, error) {
	if cluster == nil {
		return recon.Result{}, nil
	}
	if deps.Resources.Client == nil {
		return recon.Result{}, fmt.Errorf("storage client is required")
	}

	desiredQty, desiredStorageClassName, err := desiredStorageSpec(cluster)
	if err != nil {
		return recon.Result{}, err
	}
	readDesiredQty, readDesiredStorageClassName, readPoolConfigured, err := desiredReadReplicaStorageSpec(cluster, desiredQty, desiredStorageClassName)
	if err != nil {
		return recon.Result{}, err
	}

	pvcs, err := listClusterPVCs(ctx, deps.Resources.Client, cluster)
	if err != nil {
		return recon.Result{}, err
	}
	if len(pvcs.Voters) == 0 && len(pvcs.ReadReplicas) == 0 {
		return recon.Result{}, nil
	}

	if err := validateStorageChangeAllowed("spec.storage", desiredQty, desiredStorageClassName, pvcs.Voters); err != nil {
		return recon.Result{}, err
	}
	if readPoolConfigured {
		if err := validateStorageChangeAllowed("spec.readReplicas.storage", readDesiredQty, readDesiredStorageClassName, pvcs.ReadReplicas); err != nil {
			return recon.Result{}, err
		}
	}

	patched, err := expandPVCs(ctx, deps.Resources.Client, deps.Events.Recorder, cluster, logger, desiredQty, pvcs.Voters)
	if err != nil {
		return recon.Result{}, err
	}
	if patched > 0 {
		logger.Info("Requested voter PVC storage expansion", "count", patched, "desired", desiredQty.String())
	}

	if readPoolConfigured {
		readPatched, err := expandPVCs(ctx, deps.Resources.Client, deps.Events.Recorder, cluster, logger, readDesiredQty, pvcs.ReadReplicas)
		if err != nil {
			return recon.Result{}, err
		}
		if readPatched > 0 {
			logger.Info("Requested read-replica PVC storage expansion", "count", readPatched, "desired", readDesiredQty.String())
		}
	}

	return recon.Result{}, nil
}

// StoragePodClient exposes pod-targeted OpenBao API actions needed for storage restarts.
type StoragePodClient interface {
	IsLeader(ctx context.Context) (bool, error)
	StepDownLeader(ctx context.Context) error
}

// StoragePodClientFactory constructs pod-targeted OpenBao API clients.
type StoragePodClientFactory func(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (StoragePodClient, error)

// StoragePodRuntime groups pod-targeted OpenBao client construction.
type StoragePodRuntime struct {
	ClientForPodFunc StoragePodClientFactory
}

// StorageResizeRestartDependencies provides dependencies for filesystem resize restarts.
type StorageResizeRestartDependencies struct {
	Resources StorageResourceRuntime
	Events    StorageEventRuntime
	Pods      StoragePodRuntime
}

type storageResizeRestartReconciler struct {
	deps StorageResizeRestartDependencies
}

// NewStorageResizeRestartReconciler creates a SubReconciler that handles filesystem resize restarts.
func NewStorageResizeRestartReconciler(deps StorageResizeRestartDependencies) SubReconciler {
	return &storageResizeRestartReconciler{deps: deps}
}

func (r *storageResizeRestartReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	return ReconcileStorageResizeRestart(ctx, logger, cluster, r.deps)
}

// ReconcileStorageResizeRestart performs controlled pod restarts when PVC filesystem expansion is pending.
func ReconcileStorageResizeRestart(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	deps StorageResizeRestartDependencies,
) (recon.Result, error) {
	if cluster == nil || !cluster.Status.Initialized {
		return recon.Result{}, nil
	}
	if deps.Resources.Client == nil {
		return recon.Result{}, fmt.Errorf("storage restart client is required")
	}

	apiReader := deps.Resources.APIReader
	if apiReader == nil {
		apiReader = deps.Resources.Client
	}

	var pvcList corev1.PersistentVolumeClaimList
	if err := apiReader.List(ctx, &pvcList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(map[string]string{labelOpenBaoCluster: cluster.Name}),
	); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return recon.Result{}, operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to list PVCs for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err))
		}
		return recon.Result{}, fmt.Errorf("failed to list PVCs for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
	}
	pvcList.Items = filterPVCsByOwnerProof(pvcList.Items, cluster)

	if cluster.Spec.Maintenance == nil || !cluster.Spec.Maintenance.Enabled {
		if anyPVCFileSystemResizePending(pvcList.Items) {
			return recon.Result{}, operatorerrors.WithReason(
				constants.ReasonStorageRestartRequired,
				operatorerrors.WrapPermanentPrerequisitesMissing(fmt.Errorf(
					"PVC filesystem resize is pending and requires a pod restart; enable spec.maintenance.enabled=true to allow the operator to perform controlled restarts, or restart the pods manually",
				)),
			)
		}
		return recon.Result{}, nil
	}

	if cluster.Status.Phase == openbaov1alpha1.ClusterPhaseUpgrading ||
		(cluster.Status.BlueGreen != nil && cluster.Status.BlueGreen.Phase != "" && cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle) {
		return recon.Result{RequeueAfter: storageRequeueShort}, nil
	}

	targetPod, err := nextPodNeedingFSResizeRestart(ctx, deps.Resources.Client, cluster, pvcList.Items)
	if err != nil {
		return recon.Result{}, err
	}
	if targetPod == nil {
		return recon.Result{}, nil
	}

	if !isPodReady(targetPod) {
		return recon.Result{RequeueAfter: storageRequeueShort}, nil
	}

	actions, err := clientForPod(ctx, cluster, targetPod.Name, deps.Pods.ClientForPodFunc)
	if err != nil {
		return recon.Result{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to create OpenBao client for pod %s: %w", targetPod.Name, err))
	}

	isLeader, err := actions.IsLeader(ctx)
	if err != nil {
		return recon.Result{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to check leadership for pod %s: %w", targetPod.Name, err))
	}

	if isLeader {
		if cluster.Spec.Replicas > 1 {
			logger.Info("Pod requires filesystem resize restart but is leader; stepping down first", "pod", targetPod.Name)
			if err := actions.StepDownLeader(ctx); err != nil {
				return recon.Result{}, operatorerrors.WrapTransientConnection(fmt.Errorf("failed to step down leader %s before restart: %w", targetPod.Name, err))
			}
			if deps.Events.Recorder != nil {
				deps.Events.Recorder.Eventf(cluster, nil, corev1.EventTypeNormal, eventReasonPVCResizeLeaderStepDown, eventReasonPVCResizeLeaderStepDown, "Leader %s stepped down to complete filesystem resize", targetPod.Name)
			}
			return recon.Result{RequeueAfter: storageRequeueShort}, nil
		}
		logger.Info("Pod requires filesystem resize restart and is leader in a single-replica cluster; restarting without step-down", "pod", targetPod.Name)
	}

	logger.Info("Restarting pod to complete filesystem resize", "pod", targetPod.Name)
	if err := deps.Resources.Client.Delete(ctx, targetPod, client.Preconditions{UID: &targetPod.UID}); err != nil && !apierrors.IsNotFound(err) {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return recon.Result{}, operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to delete pod %s/%s for filesystem resize restart: %w", targetPod.Namespace, targetPod.Name, err))
		}
		return recon.Result{}, fmt.Errorf("failed to delete pod %s/%s for filesystem resize restart: %w", targetPod.Namespace, targetPod.Name, err)
	}

	if deps.Events.Recorder != nil {
		deps.Events.Recorder.Eventf(cluster, nil, corev1.EventTypeNormal, eventReasonPVCResizePodRestart, eventReasonPVCResizePodRestart, "Restarted pod %s to complete filesystem resize", targetPod.Name)
	}

	return recon.Result{RequeueAfter: storageRequeueShort}, nil
}

func filterPVCsByOwnerProof(pvcs []corev1.PersistentVolumeClaim, cluster *openbaov1alpha1.OpenBaoCluster) []corev1.PersistentVolumeClaim {
	owned := pvcs[:0]
	for i := range pvcs {
		pvc := pvcs[i]
		if resourceownership.HasOwnerProof(&pvc, cluster) {
			owned = append(owned, pvc)
		}
	}
	return owned
}
