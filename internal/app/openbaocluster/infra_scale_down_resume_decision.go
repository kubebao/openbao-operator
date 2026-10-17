package openbaocluster

import (
	"fmt"
	"time"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

type scaleDownResumePoolDecision struct {
	name, revision string
	replicas       int32
	skip           bool
}

// This checkpoint contains scale decisions and operation identity, not status
// observations or lock renewal timestamps that can change during recovery.
type scaleDownResumeDecision struct {
	voter, reader                         scaleDownResumePoolDecision
	strategy                              openbaov1alpha1.UpdateStrategyType
	readDrain, blueGreen, locked, rolling bool
	phase                                 openbaov1alpha1.BlueGreenPhase
	operationID, controllerRevision       string
	greenRevision                         string
	greenReplicas                         int32
	operation                             openbaov1alpha1.ClusterOperation
	holder, acquiredAt, restoreOrigin     string
	fromVersion, targetVersion, startedAt string
	executionID                           string
	partition                             int32
}

func (r *infraReconciler) scaleDownResumeDecision(cluster *openbaov1alpha1.OpenBaoCluster) scaleDownResumeDecision {
	voter := r.computeStatefulSetSpec(logr.Discard(), cluster, "", "")
	reader := r.computeReadReplicaStatefulSetSpec(cluster, "", "")
	decision := scaleDownResumeDecision{
		voter:         scaleDownResumePoolDecision{voter.Name, voter.Revision, voter.Replicas, voter.SkipReconciliation},
		strategy:      upgrade.EffectiveStrategy(cluster),
		readDrain:     applyOperationalReadReplicaStageDown(cluster, &reader),
		restoreOrigin: cluster.Annotations[constants.AnnotationRestoreOrigin],
	}
	decision.reader = scaleDownResumePoolDecision{reader.Name, reader.Revision, reader.Replicas, reader.SkipReconciliation}
	if bg := cluster.Status.BlueGreen; bg != nil {
		decision.blueGreen, decision.phase, decision.operationID = true, bg.Phase, bg.OperationID
		decision.controllerRevision = bg.BlueControllerRevision
		decision.greenRevision, decision.greenReplicas = bg.GreenRevision, bg.GreenReplicas
	}
	if lock := cluster.Status.OperationLock; lock != nil {
		decision.locked, decision.operation, decision.holder = true, lock.Operation, lock.Holder
		if lock.AcquiredAt != nil {
			decision.acquiredAt = lock.AcquiredAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if rolling := cluster.Status.Upgrade; rolling != nil {
		decision.rolling, decision.fromVersion, decision.targetVersion = true, rolling.FromVersion, rolling.TargetVersion
		decision.executionID = rolling.ExecutionID
		decision.partition = rolling.CurrentPartition
		if rolling.StartedAt != nil {
			decision.startedAt = rolling.StartedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return decision
}

func (r *infraReconciler) validateScaleDownResumeCluster(
	observed, current *openbaov1alpha1.OpenBaoCluster, workloadName string, desired int32, readReplica bool,
) error {
	if observed.UID == "" || current.UID != observed.UID || current.Generation != observed.Generation ||
		!current.DeletionTimestamp.IsZero() || !current.Status.Initialized || current.Spec.Paused ||
		current.Annotations[constants.AnnotationRestoreHold] != "" ||
		(current.Status.BreakGlass != nil && current.Status.BreakGlass.Active) {
		return fmt.Errorf("cluster identity or recovery authority changed before scale-down resume")
	}
	decision := r.scaleDownResumeDecision(current)
	if decision.operation == openbaov1alpha1.ClusterOperationRestore || decision != r.scaleDownResumeDecision(observed) {
		return fmt.Errorf("cluster scale decision or operation changed before scale-down resume")
	}
	pool := decision.voter
	if readReplica {
		pool = decision.reader
	} else if pool.skip {
		return fmt.Errorf("current voter workload reconciliation is suspended")
	}
	if pool.name != workloadName || pool.replicas != desired {
		return fmt.Errorf("workload no longer matches the current scale-down decision")
	}
	return upgrade.ValidateUpgradeTargetVersion(logr.Discard(), current.Status.CurrentVersion, current.Spec.Version)
}
