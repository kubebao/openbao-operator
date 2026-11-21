package workload

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

func (m *Manager) prepareVoterTemplateCorrection(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, existing, desired *appsv1.StatefulSet, fingerprint string) error {
	// Complete the published replica count and version before changing either.
	if *existing.Spec.Replicas != cluster.Spec.Replicas ||
		(cluster.Status.CurrentVersion != "" && cluster.Status.CurrentVersion != cluster.Spec.Version) {
		if previous := existing.Annotations[constants.AnnotationVoterRolloutTemplate]; strings.HasPrefix(previous, deferredVoterTemplate) {
			if err := m.holdVoterTemplate(ctx, cluster, existing, constants.AnnotationVoterTemplateRepair, strings.TrimPrefix(previous, deferredVoterTemplate)); err != nil {
				return err
			}
		}
		return portworkload.ErrVoterTemplateRolloutPending
	}
	repair, err := m.voterTemplateNeedsRepair(ctx, existing)
	if err != nil {
		return err
	}
	if !repair {
		return portworkload.ErrVoterTemplateRolloutPending
	}
	if err := m.holdVoterTemplate(ctx, cluster, existing, constants.AnnotationVoterTemplateRepair, fingerprint); err != nil {
		return err
	}
	desired.Annotations[constants.AnnotationVoterRolloutTemplate] = fingerprint
	return nil
}

func (m *Manager) voterTemplateNeedsRepair(ctx context.Context, sts *appsv1.StatefulSet) (bool, error) {
	if sts.Annotations[constants.AnnotationVoterTemplateRepair] != "" ||
		strings.HasPrefix(sts.Annotations[constants.AnnotationVoterRolloutTemplate], deferredVoterTemplate) {
		return true, nil
	}
	for ordinal := int32(0); ordinal < *sts.Spec.Replicas; ordinal++ {
		pod := &corev1.Pod{}
		if err := m.reader.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, ordinal)}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if pod.DeletionTimestamp != nil || !isPodReady(pod) {
			return true, nil
		}
	}
	return false, nil
}

// OnDelete keeps each existing revision in place while a correction is staged.
// Repair an unavailable old revision first, then the highest remaining ordinal.
// The desired revision is the durable receipt for each replacement: a slow new
// Pod is never deleted again, including after a lost delete response or restart.
func (m *Manager) reconcileVoterTemplateRepair(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet) (recon.Result, error) {
	if sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	var target *corev1.Pod
	for ordinal := *sts.Spec.Replicas - 1; ordinal >= 0; ordinal-- {
		pod := &corev1.Pod{}
		if err := m.reader.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, ordinal)}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				return recon.Result{RequeueAfter: constants.RequeueShort}, nil
			}
			return recon.Result{}, err
		}
		if pod.Labels[appsv1.StatefulSetRevisionLabel] == sts.Status.UpdateRevision {
			continue
		}
		ready := pod.DeletionTimestamp == nil && isPodReady(pod)
		if target == nil || !ready {
			target = pod
		}
		if !ready {
			break
		}
	}
	if target != nil {
		if target.DeletionTimestamp != nil {
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		if err := m.CheckVoterRollout(ctx, logger, cluster, sts, target.Name, true); err != nil {
			return m.voterRolloutHeld(logger, cluster, target.Name, err)
		}
		if err := m.client.Delete(ctx, target, client.Preconditions{UID: &target.UID, ResourceVersion: &target.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return recon.Result{}, err
		}
		logger.Info("Replacing voter for corrected template", "pod", target.Name, "uid", target.UID)
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	if err := m.CheckVoterRollout(ctx, logger, cluster, sts, "", false); err != nil {
		return m.voterRolloutHeld(logger, cluster, "", err)
	}
	live := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(sts), live); err != nil {
		return recon.Result{}, err
	}
	if live.UID != sts.UID || live.Generation != sts.Generation || live.Status.UpdateRevision != sts.Status.UpdateRevision {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	updated := live.DeepCopy()
	partition := int32(0)
	updated.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}}
	delete(updated.Annotations, constants.AnnotationVoterTemplateRepair)
	delete(updated.Annotations, constants.AnnotationVoterRolloutObservation)
	if err := m.client.Patch(ctx, updated, client.MergeFromWithOptions(live, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
		return recon.Result{}, err
	}
	return recon.Result{RequeueAfter: constants.RequeueShort}, nil
}
