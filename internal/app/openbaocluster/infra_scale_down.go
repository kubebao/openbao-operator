package openbaocluster

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

func (r *infraReconciler) handleScaleDownSafety(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, desiredReplicas int32, currentSTS *appsv1.StatefulSet) (int32, error) {
	if currentSTS.Spec.Replicas == nil {
		return desiredReplicas, nil
	}
	currentReplicas := *currentSTS.Spec.Replicas
	if desiredReplicas >= currentReplicas {
		return desiredReplicas, nil
	}
	if !statefulSetSettledAtReplicas(currentSTS, currentReplicas) {
		return currentReplicas, fmt.Errorf(
			"waiting for StatefulSet %s/%s to settle at %d replicas before next scale-down step",
			currentSTS.Namespace,
			currentSTS.Name,
			currentReplicas,
		)
	}

	if r.deps.ScaleDown.Runtime == nil {
		return currentReplicas, fmt.Errorf("scale-down runtime is not configured")
	}

	nextReplicas := currentReplicas - 1
	if nextReplicas < desiredReplicas {
		nextReplicas = desiredReplicas
	}

	victimOrdinal := currentReplicas - 1
	victimPodName := fmt.Sprintf("%s-%d", currentSTS.Name, victimOrdinal)

	logger := log.FromContext(ctx).WithValues(
		"victim", victimPodName,
		"currentReplicas", currentReplicas,
		"desiredReplicas", desiredReplicas,
		"appliedReplicas", nextReplicas,
	)
	logger.Info("Detected scale down operation; preparing safe replica decrement")

	if err := r.deps.ScaleDown.Runtime.PrepareScaleDown(ctx, logger, cluster, currentSTS.Name, currentReplicas, nextReplicas); err != nil {
		return currentReplicas, err
	}

	logger.Info("Safe scale down step prepared")
	return nextReplicas, nil
}

func (r *infraReconciler) handleReadReplicaScaleDownSafety(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, desiredReplicas int32, currentSTS *appsv1.StatefulSet) (int32, error) {
	if currentSTS.Spec.Replicas == nil {
		return desiredReplicas, nil
	}
	currentReplicas := *currentSTS.Spec.Replicas
	if desiredReplicas >= currentReplicas {
		return desiredReplicas, nil
	}
	if !cluster.Status.Initialized {
		return desiredReplicas, nil
	}
	if !statefulSetSettledAtReplicas(currentSTS, currentReplicas) {
		return currentReplicas, fmt.Errorf(
			"waiting for StatefulSet %s/%s to settle at %d replicas before next read-replica scale-down step",
			currentSTS.Namespace,
			currentSTS.Name,
			currentReplicas,
		)
	}

	if r.deps.ScaleDown.ReadReplicaRuntime == nil {
		return currentReplicas, fmt.Errorf("read-replica scale-down runtime is not configured")
	}

	nextReplicas := currentReplicas - 1
	if nextReplicas < desiredReplicas {
		nextReplicas = desiredReplicas
	}

	victimOrdinal := currentReplicas - 1
	victimPodName := fmt.Sprintf("%s-%d", currentSTS.Name, victimOrdinal)

	logger := log.FromContext(ctx).WithValues(
		"victim", victimPodName,
		"currentReplicas", currentReplicas,
		"desiredReplicas", desiredReplicas,
		"appliedReplicas", nextReplicas,
	)
	logger.Info("Detected read-replica scale down operation; preparing safe replica decrement")

	if err := r.deps.ScaleDown.ReadReplicaRuntime.PrepareReadReplicaScaleDown(ctx, logger, cluster, currentSTS.Name, currentReplicas, nextReplicas); err != nil {
		return currentReplicas, err
	}

	logger.Info("Safe read-replica scale down step prepared")
	return nextReplicas, nil
}

func statefulSetSettledAtReplicas(sts *appsv1.StatefulSet, replicas int32) bool {
	if sts == nil {
		return false
	}
	if sts.Status.ObservedGeneration < sts.Generation {
		return false
	}
	if sts.Status.Replicas != replicas {
		return false
	}
	if sts.Status.ReadyReplicas != replicas {
		return false
	}
	return sts.Status.CurrentReplicas == replicas
}

func scaleDownWaitResult(logger logr.Logger, err error, readReplica bool) recon.Result {
	if operatorerrors.IsPermanent(err) && errors.Is(err, operatorerrors.ErrPermanentPrerequisitesMissing) {
		logger.Info("Scale down requires user intervention", "readReplica", readReplica, "reason", err.Error())
		return recon.Result{}
	}
	logger.Info("Scale down safety check blocked reconciliation", "readReplica", readReplica, "reason", err.Error())
	return recon.Result{RequeueAfter: infraRequeueShort}
}
