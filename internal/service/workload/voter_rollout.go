package workload

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

const deferredVoterTemplate = "deferred:"

// prepareVoterTemplateRollout freezes the live partition before publishing a
// changed template. The template digest uses the rendered input, avoiding API
// defaults and detecting field removals as well as additions.
func (m *Manager) prepareVoterTemplateRollout(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec, existing, desired *appsv1.StatefulSet) error {
	if spec.Pool != constants.LabelValueOpenBaoWorkloadPoolVoter || !cluster.Status.Initialized || cluster.Spec.Replicas <= 1 ||
		portworkload.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyRollingUpdate ||
		existing.Spec.Replicas == nil || *existing.Spec.Replicas <= 1 {
		return nil
	}
	data, err := json.Marshal(desired.Spec.Template)
	if err != nil {
		return err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(data))
	previous := existing.Annotations[constants.AnnotationVoterRolloutTemplate]
	if previous == fingerprint || previous == deferredVoterTemplate+fingerprint {
		desired.Annotations[constants.AnnotationVoterRolloutTemplate] = previous
		return nil
	}
	// An idle BlueGreen -> RollingUpdate switch must retain the promoted Pods
	// when their OnDelete template already differed before the switch.
	if existing.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType &&
		existing.Status.CurrentRevision != existing.Status.UpdateRevision &&
		apiequality.Semantic.DeepDerivative(desired.Spec.Template, existing.Spec.Template) {
		desired.Annotations[constants.AnnotationVoterRolloutTemplate] = deferredVoterTemplate + fingerprint
		return nil
	}
	// Raising a partition during replacement can recreate an already-upgraded
	// Pod from currentRevision, implicitly downgrading it. Keep the old rollout
	// and its config intact until it finishes before publishing a new template.
	if cluster.Status.Upgrade != nil || (previous != "" && existing.Status.ObservedGeneration < existing.Generation) ||
		(existing.Status.CurrentRevision != "" && existing.Status.UpdateRevision != "" &&
			existing.Status.CurrentRevision != existing.Status.UpdateRevision) {
		return portworkload.ErrVoterTemplateRolloutPending
	}
	live := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
		return err
	}
	if live.UID != existing.UID || live.ResourceVersion != existing.ResourceVersion {
		return apierrors.NewConflict(appsv1.Resource("statefulsets"), live.Name, fmt.Errorf("StatefulSet changed before locking voter template"))
	}
	if err := resourceownership.RequireOwnerProof("lock voter template rollout", live, cluster); err != nil {
		return err
	}
	partition := max(cluster.Spec.Replicas, *existing.Spec.Replicas)
	locked := live.DeepCopy()
	locked.Spec.UpdateStrategy.Type = appsv1.RollingUpdateStatefulSetStrategyType
	if locked.Spec.UpdateStrategy.RollingUpdate == nil {
		locked.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{}
	}
	locked.Spec.UpdateStrategy.RollingUpdate.Partition = &partition
	delete(locked.Annotations, constants.AnnotationVoterRolloutObservation)
	if !apiequality.Semantic.DeepEqual(live.Spec.UpdateStrategy, locked.Spec.UpdateStrategy) || live.Annotations[constants.AnnotationVoterRolloutObservation] != "" {
		if err := m.client.Patch(ctx, locked, client.MergeFromWithOptions(live, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
			return fmt.Errorf("lock voter partition before changing template: %w", err)
		}
		*existing = *locked.DeepCopy()
	}
	desired.Annotations[constants.AnnotationVoterRolloutTemplate] = fingerprint
	return nil
}

// ReconcileVoterRollout advances ordinary template replacements after init and
// unseal reconciliation. Managed version upgrades retain their own partition owner.
func (m *Manager) ReconcileVoterRollout(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	if skipVoterRollout(cluster) {
		return recon.Result{}, nil
	}
	sts := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: portworkload.StableVoterStatefulSetName(cluster)}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return recon.Result{}, nil
		}
		return recon.Result{}, err
	}
	marker := sts.Annotations[constants.AnnotationVoterRolloutTemplate]
	if marker == "" || strings.HasPrefix(marker, deferredVoterTemplate) {
		return recon.Result{}, nil
	}
	if cluster.Status.CurrentVersion != "" && cluster.Status.CurrentVersion != cluster.Spec.Version &&
		sts.Annotations[constants.AnnotationClusterGeneration] == strconv.FormatInt(cluster.Generation, 10) {
		return recon.Result{}, nil // AdminOps owns the published version-change template.
	}
	if err := resourceownership.RequireOwnerProof("advance voter template rollout", sts, cluster); err != nil {
		return recon.Result{}, err
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != cluster.Spec.Replicas || sts.Status.ObservedGeneration < sts.Generation || sts.Status.UpdateRevision == "" {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	partition := int32(0)
	if sts.Spec.UpdateStrategy.RollingUpdate != nil && sts.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
		partition = *sts.Spec.UpdateStrategy.RollingUpdate.Partition
	}
	if partition == 0 {
		return recon.Result{}, nil // No further voter can be released by this rollout.
	}
	next := int32(-1)
	for ordinal := *sts.Spec.Replicas - 1; ordinal >= 0; ordinal-- {
		pod := &corev1.Pod{}
		if err := m.reader.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, ordinal)}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				return recon.Result{RequeueAfter: constants.RequeueShort}, nil
			}
			return recon.Result{}, err
		}
		if pod.Labels[appsv1.StatefulSetRevisionLabel] != sts.Status.UpdateRevision {
			next = ordinal
			break
		}
	}
	if next >= 0 {
		// Never raise an in-flight partition: a missing replacement must still
		// be created from the update revision rather than the previous template.
		if partition <= next {
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		target := fmt.Sprintf("%s-%d", sts.Name, next)
		if err := m.CheckVoterRollout(ctx, logger, cluster, sts, target, true); err != nil {
			if errors.Is(err, portworkload.ErrVoterRolloutWaiting) {
				logger.Info("Holding voter template rollout", "target", target, "reason", err.Error())
				if m.recorder != nil {
					m.recorder.Eventf(cluster, nil, corev1.EventTypeNormal, constants.ReasonVoterRolloutWaiting,
						"ReconcileVoterRollout", "Holding replacement of %s: %s", target, err.Error())
				}
				return recon.Result{RequeueAfter: constants.RequeueShort}, nil
			}
			return recon.Result{}, err
		}
		partition = next
	} else {
		partition = 0
	}
	updated := sts.DeepCopy()
	updated.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}}
	if err := m.client.Patch(ctx, updated, client.MergeFromWithOptions(sts, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
		return recon.Result{}, fmt.Errorf("advance voter template partition: %w", err)
	}
	logger.Info("Advanced voter template rollout", "statefulset", sts.Name, "partition", partition)
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}

func skipVoterRollout(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Paused || cluster.DeletionTimestamp != nil ||
		(cluster.Status.BreakGlass != nil && cluster.Status.BreakGlass.Active && cluster.Spec.BreakGlassAck != cluster.Status.BreakGlass.Nonce) ||
		!cluster.Status.Initialized || cluster.Spec.Replicas <= 1 || cluster.Status.Upgrade != nil ||
		portworkload.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyRollingUpdate
}
