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
	if spec.Pool != constants.LabelValueOpenBaoWorkloadPoolVoter || !cluster.Status.Initialized ||
		portworkload.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyRollingUpdate ||
		existing.Spec.Replicas == nil || *existing.Spec.Replicas <= 1 {
		return nil
	}
	data, err := json.Marshal(desired.Spec.Template)
	if err != nil {
		return err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(data))
	if accepted, err := m.prepareMatchingVoterTemplate(ctx, cluster, existing, desired, fingerprint); err != nil || accepted {
		return err
	}
	previous := existing.Annotations[constants.AnnotationVoterRolloutTemplate]
	if cluster.Status.Upgrade != nil && cluster.Status.Upgrade.Failure != nil {
		if err := m.holdVoterTemplate(ctx, cluster, existing, constants.AnnotationVoterRetryHold, "true"); err != nil {
			return err
		}
		desired.Annotations[constants.AnnotationVoterRolloutTemplate] = fingerprint
		return nil
	}
	// An idle BlueGreen -> RollingUpdate switch must retain the promoted Pods
	// when their OnDelete template already differed before the switch.
	if isIdlePromotedVoterTemplate(cluster, existing, desired) {
		desired.Annotations[constants.AnnotationVoterRolloutTemplate] = deferredVoterTemplate + fingerprint
		return nil
	}
	// Raising a partition during replacement can recreate an already-upgraded
	// Pod from currentRevision, implicitly downgrading it. Keep the old rollout
	// and its config intact until it finishes before publishing a new template.
	if cluster.Status.Upgrade != nil || (previous != "" && existing.Status.ObservedGeneration < existing.Generation) {
		return portworkload.ErrVoterTemplateRolloutPending
	}
	if existing.Annotations[constants.AnnotationVoterTemplateRepair] != "" ||
		(existing.Status.CurrentRevision != "" && existing.Status.UpdateRevision != "" && existing.Status.CurrentRevision != existing.Status.UpdateRevision) {
		return m.prepareVoterTemplateCorrection(ctx, cluster, existing, desired, fingerprint)
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

func (m *Manager) prepareMatchingVoterTemplate(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, existing, desired *appsv1.StatefulSet, fingerprint string) (bool, error) {
	previous := existing.Annotations[constants.AnnotationVoterRolloutTemplate]
	if previous != fingerprint && previous != deferredVoterTemplate+fingerprint {
		return false, nil
	}
	// A publication failure can leave a hold for a template that never reached
	// the StatefulSet. Rebind it on revert, keeping OnDelete so guarded repair
	// can finish the published template before any pending scale.
	if repair := existing.Annotations[constants.AnnotationVoterTemplateRepair]; repair != "" && repair != fingerprint {
		if err := m.holdVoterTemplate(ctx, cluster, existing, constants.AnnotationVoterTemplateRepair, fingerprint); err != nil {
			return false, err
		}
	}
	if !voterTemplateScaleAllowed(existing, cluster.Spec.Replicas) {
		return false, nil
	}
	desired.Annotations[constants.AnnotationVoterRolloutTemplate] = existing.Annotations[constants.AnnotationVoterRolloutTemplate]
	return true, nil
}

// A matching template does not release a replica change during an active
// rollout or repair. A settled workload can scale without locking a new template.
func voterTemplateScaleAllowed(existing *appsv1.StatefulSet, replicas int32) bool {
	return *existing.Spec.Replicas == replicas ||
		(existing.Annotations[constants.AnnotationVoterTemplateRepair] == "" && existing.Status.CurrentRevision == existing.Status.UpdateRevision)
}

func isIdlePromotedVoterTemplate(cluster *openbaov1alpha1.OpenBaoCluster, existing, desired *appsv1.StatefulSet) bool {
	return cluster.Status.Upgrade == nil && existing.Annotations[constants.AnnotationVoterTemplateRepair] == "" &&
		!strings.HasPrefix(existing.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate) &&
		existing.Annotations[constants.AnnotationVoterRetryHold] == "" && existing.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType &&
		existing.Status.CurrentRevision != existing.Status.UpdateRevision &&
		apiequality.Semantic.DeepDerivative(desired.Spec.Template, existing.Spec.Template)
}

// Stage a corrected template without raising an in-flight partition or allowing
// Kubernetes to advance on readiness alone. Guarded repairs own replacements
// until every voter has the requested template and fresh Raft health.
func (m *Manager) holdVoterTemplate(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, existing *appsv1.StatefulSet, annotation, value string) error {
	live := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
		return err
	}
	if live.UID != existing.UID || live.ResourceVersion != existing.ResourceVersion {
		return apierrors.NewConflict(appsv1.Resource("statefulsets"), live.Name, fmt.Errorf("StatefulSet changed before holding template repair"))
	}
	if err := resourceownership.RequireOwnerProof("hold voter template repair", live, cluster); err != nil {
		return err
	}
	held := live.DeepCopy()
	held.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	if held.Annotations == nil {
		held.Annotations = map[string]string{}
	}
	held.Annotations[annotation] = value
	if annotation == constants.AnnotationVoterTemplateRepair && held.Annotations[constants.AnnotationVoterRolloutTemplate] == deferredVoterTemplate+value {
		// Activate the published template while a requested version or replica
		// count waits. OnDelete avoids recreating promoted Pods at currentRevision.
		held.Annotations[constants.AnnotationVoterRolloutTemplate] = value
	}
	delete(held.Annotations, constants.AnnotationVoterRolloutObservation)
	if !apiequality.Semantic.DeepEqual(live.Spec.UpdateStrategy, held.Spec.UpdateStrategy) || !apiequality.Semantic.DeepEqual(live.Annotations, held.Annotations) {
		if err := m.client.Patch(ctx, held, client.MergeFromWithOptions(live, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
			return err
		}
		*existing = *held.DeepCopy()
	}
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
	if marker == "" || (strings.HasPrefix(marker, deferredVoterTemplate) && sts.Annotations[constants.AnnotationVoterTemplateRepair] == "") {
		return recon.Result{}, nil
	}
	if cluster.Status.CurrentVersion != "" && cluster.Status.CurrentVersion != cluster.Spec.Version &&
		sts.Annotations[constants.AnnotationClusterGeneration] == strconv.FormatInt(cluster.Generation, 10) {
		return recon.Result{}, nil // AdminOps owns the published version-change template.
	}
	if err := resourceownership.RequireOwnerProof("advance voter template rollout", sts, cluster); err != nil {
		return recon.Result{}, err
	}
	if sts.Spec.Replicas == nil || sts.Status.ObservedGeneration < sts.Generation || sts.Status.UpdateRevision == "" {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	if *sts.Spec.Replicas <= 1 {
		return recon.Result{}, nil
	}
	// A pending template can include a scale request. Continue the published
	// rollout with its actual voter count, without applying the deferred spec.
	cluster = cluster.DeepCopy()
	cluster.Spec.Replicas = *sts.Spec.Replicas
	if sts.Annotations[constants.AnnotationVoterTemplateRepair] != "" {
		if sts.Annotations[constants.AnnotationVoterTemplateRepair] != marker {
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		return m.reconcileVoterTemplateRepair(ctx, logger, cluster, sts)
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
			return m.voterRolloutHeld(logger, cluster, target, err)
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
		!cluster.Status.Initialized || cluster.Status.Upgrade != nil ||
		portworkload.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyRollingUpdate
}

func (m *Manager) voterRolloutHeld(logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, target string, err error) (recon.Result, error) {
	if !errors.Is(err, portworkload.ErrVoterRolloutWaiting) {
		return recon.Result{}, err
	}
	logger.Info("Holding voter template rollout", "target", target, "reason", err.Error())
	if m.recorder != nil {
		m.recorder.Eventf(cluster, nil, corev1.EventTypeNormal, constants.ReasonVoterRolloutWaiting,
			"ReconcileVoterRollout", "Holding replacement of %s: %s", target, err.Error())
	}
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}
